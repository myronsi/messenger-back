package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

const (
	ChunkSize          = 8 << 20
	MaxOpenUploads     = 5
	UploadSessionTTL   = 24 * time.Hour
	UploadSessionIdle  = 15 * time.Minute
	uploadChunksPrefix = "uploads/"
)

var (
	ErrUploadOffset     = errors.New("media: upload offset does not match")
	ErrUploadIncomplete = errors.New("media: upload is not complete")
	ErrTooManyUploads   = errors.New("media: too many open uploads")
)

type NewSession struct {
	UploaderID int64
	Purpose    string
	Kind       string
	Filename   string
	Size       int64
}

func chunkKey(id uuid.UUID, index int) string {
	return uploadChunksPrefix + id.String() + "/" + strconv.Itoa(index)
}

func (s *Service) sessions() (postgres.UploadSessionRepository, error) {
	if s.o.Sessions == nil {
		return nil, fmt.Errorf("%w: resumable uploads are not configured", ErrUnsupported)
	}
	return s.o.Sessions, nil
}

func (s *Service) CreateSession(ctx context.Context, n NewSession) (postgres.UploadSession, error) {
	repo, err := s.sessions()
	if err != nil {
		return postgres.UploadSession{}, err
	}
	switch n.Purpose {
	case PurposeMessage, PurposeAvatar, PurposeProfileMusic, PurposeProfileBackground:
	default:
		return postgres.UploadSession{}, fmt.Errorf("%w: purpose", ErrInvalid)
	}
	if n.Size <= 0 {
		return postgres.UploadSession{}, fmt.Errorf("%w: size", ErrInvalid)
	}
	if n.Size > s.o.Limits.Max() {
		return postgres.UploadSession{}, ErrTooLarge
	}
	open, err := repo.Count(ctx, n.UploaderID)
	if err != nil {
		return postgres.UploadSession{}, err
	}
	if open >= MaxOpenUploads {
		idle, err := repo.IdleOf(ctx, n.UploaderID, time.Now().Add(-UploadSessionIdle), int(open-MaxOpenUploads+1))
		if err != nil {
			return postgres.UploadSession{}, err
		}
		for _, sess := range idle {
			s.dropSession(ctx, sess)
		}
		if open-int64(len(idle)) >= MaxOpenUploads {
			return postgres.UploadSession{}, ErrTooManyUploads
		}
	}
	return repo.Create(ctx, postgres.NewUploadSession{
		UploaderID: n.UploaderID, Purpose: n.Purpose, Kind: n.Kind, Filename: cleanName(n.Filename, ""), Size: n.Size, ChunkSize: ChunkSize,
	})
}

func (s *Service) GetSession(ctx context.Context, id uuid.UUID, uploaderID int64) (postgres.UploadSession, error) {
	repo, err := s.sessions()
	if err != nil {
		return postgres.UploadSession{}, err
	}
	sess, err := repo.Get(ctx, id, uploaderID)
	if errors.Is(err, postgres.ErrNotFound) {
		return postgres.UploadSession{}, ErrNoAccess
	}
	return sess, err
}

func (s *Service) PutChunk(ctx context.Context, id uuid.UUID, uploaderID, offset int64, body io.Reader) (postgres.UploadSession, error) {
	sess, err := s.GetSession(ctx, id, uploaderID)
	if err != nil {
		return postgres.UploadSession{}, err
	}
	if offset != sess.Received {
		return sess, ErrUploadOffset
	}
	want := min(int64(sess.ChunkSize), sess.Size-sess.Received)
	if want <= 0 {
		return sess, ErrUploadOffset
	}
	tmp, size, cleanup, err := spool(body, want)
	if err != nil {
		return sess, err
	}
	defer cleanup()
	if size > want {
		return sess, ErrTooLarge
	}
	if size != want {
		return sess, fmt.Errorf("%w: a chunk is %d bytes", ErrInvalid, want)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return sess, err
	}
	key := chunkKey(id, sess.Chunks)
	if err := s.o.Storage.Put(ctx, key, tmp, size, "application/octet-stream"); err != nil {
		return sess, err
	}
	next, err := s.o.Sessions.Advance(ctx, id, uploaderID, offset, size)
	if errors.Is(err, postgres.ErrUploadOffset) {
		current, gerr := s.GetSession(ctx, id, uploaderID)
		if gerr != nil {
			return sess, gerr
		}
		return current, ErrUploadOffset
	}
	return next, err
}

func (s *Service) CompleteSession(ctx context.Context, id uuid.UUID, uploaderID int64) (postgres.Attachment, Tags, error) {
	sess, err := s.GetSession(ctx, id, uploaderID)
	if err != nil {
		return postgres.Attachment{}, Tags{}, err
	}
	if sess.Received != sess.Size {
		return postgres.Attachment{}, Tags{}, ErrUploadIncomplete
	}
	tmp, err := os.CreateTemp("", "messenger-upload-*")
	if err != nil {
		return postgres.Attachment{}, Tags{}, err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	for i := range sess.Chunks {
		obj, err := s.o.Storage.Get(ctx, chunkKey(id, i))
		if err != nil {
			return postgres.Attachment{}, Tags{}, err
		}
		_, err = io.Copy(tmp, io.LimitReader(obj.Body, int64(sess.ChunkSize)+1))
		_ = obj.Body.Close()
		if err != nil {
			return postgres.Attachment{}, Tags{}, err
		}
	}
	if st, err := tmp.Stat(); err != nil || st.Size() != sess.Size {
		return postgres.Attachment{}, Tags{}, fmt.Errorf("%w: assembled size", ErrInvalid)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return postgres.Attachment{}, Tags{}, err
	}
	a, tags, err := s.UploadWithTags(ctx, Upload{UploaderID: uploaderID, Purpose: sess.Purpose, Kind: sess.Kind, Filename: sess.Filename, Body: tmp})
	if err != nil {
		return postgres.Attachment{}, Tags{}, err
	}
	s.dropSession(ctx, sess)
	return a, tags, nil
}

func (s *Service) CancelSession(ctx context.Context, id uuid.UUID, uploaderID int64) error {
	sess, err := s.GetSession(ctx, id, uploaderID)
	if err != nil {
		return err
	}
	s.dropSession(ctx, sess)
	return nil
}

func (s *Service) dropSession(ctx context.Context, sess postgres.UploadSession) {
	ctx = context.WithoutCancel(ctx)
	if _, err := s.o.Sessions.Delete(ctx, sess.ID); err != nil && !errors.Is(err, postgres.ErrNotFound) {
		s.o.Log.WarnContext(ctx, "delete upload session", "error", err)
		return
	}
	for i := range sess.Chunks + 1 {
		if err := s.o.Storage.Delete(ctx, chunkKey(sess.ID, i)); err != nil && !errors.Is(err, ErrNotFound) {
			s.o.Log.WarnContext(ctx, "delete upload chunk", "error", err)
		}
	}
}

func (s *Service) CollectSessions(ctx context.Context, batch int) (int, error) {
	if s.o.Sessions == nil {
		return 0, nil
	}
	stale, err := s.o.Sessions.Stale(ctx, time.Now().Add(-UploadSessionTTL), batch)
	if err != nil {
		return 0, err
	}
	for _, sess := range stale {
		s.dropSession(ctx, sess)
	}
	return len(stale), nil
}
