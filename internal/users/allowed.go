package users

import (
	"context"

	"github.com/myronsi/messenger-back/internal/store/postgres"
)

func settingValue(s postgres.PrivacySettings, key string) string {
	switch key {
	case postgres.SettingVoiceVideo:
		return s.VoiceVideoMessages
	case postgres.SettingForwardLink:
		return s.ForwardLink
	}
	return postgres.VisibleNobody
}

func AllowedViewers(ctx context.Context, social postgres.SocialRepository, key string, subject int64, viewers []int64) (map[int64]bool, error) {
	out := make(map[int64]bool, len(viewers))
	if len(viewers) == 0 {
		return out, nil
	}
	settings, err := social.Privacy(ctx, []int64{subject})
	if err != nil {
		return nil, err
	}
	value := settingValue(settings[subject], key)
	exceptions, err := social.ExceptionsOf(ctx, subject, viewers)
	if err != nil {
		return nil, err
	}
	shared := map[int64]bool{}
	if value == postgres.VisibleSharedChats {
		if shared, err = social.SharedChatViewers(ctx, subject, viewers); err != nil {
			return nil, err
		}
	}
	blocks, err := social.Blocks(ctx, []int64{subject}, viewers)
	if err != nil {
		return nil, err
	}
	for _, v := range viewers {
		out[v] = v == subject || (!blocks[[2]int64{subject, v}] && Visible(value, exceptions[v][key], shared[v]))
	}
	return out, nil
}

func AllowedSubjects(ctx context.Context, social postgres.SocialRepository, key string, viewer int64, subjects []int64) (map[int64]bool, error) {
	out := make(map[int64]bool, len(subjects))
	if len(subjects) == 0 {
		return out, nil
	}
	settings, err := social.Privacy(ctx, subjects)
	if err != nil {
		return nil, err
	}
	exceptions, err := social.ExceptionsFor(ctx, subjects, viewer)
	if err != nil {
		return nil, err
	}
	shared, err := social.SharedChatPartners(ctx, viewer, subjects)
	if err != nil {
		return nil, err
	}
	blocks, err := social.Blocks(ctx, subjects, []int64{viewer})
	if err != nil {
		return nil, err
	}
	for _, s := range subjects {
		out[s] = s == viewer || (!blocks[[2]int64{s, viewer}] && Visible(settingValue(settings[s], key), exceptions[s][key], shared[s]))
	}
	return out, nil
}

func (d *Directory) ForwardLinks(ctx context.Context, viewer int64, subjects []int64) (map[int64]bool, error) {
	return AllowedSubjects(ctx, d.store.Social(), postgres.SettingForwardLink, viewer, subjects)
}

func (d *Directory) VoiceVideoAllowed(ctx context.Context, viewer int64, subjects []int64) (map[int64]bool, error) {
	return AllowedSubjects(ctx, d.store.Social(), postgres.SettingVoiceVideo, viewer, subjects)
}

func (d *Directory) ForwardLinkViewers(ctx context.Context, subject int64, viewers []int64) (map[int64]bool, error) {
	return AllowedViewers(ctx, d.store.Social(), postgres.SettingForwardLink, subject, viewers)
}
