package media

import (
	"context"

	"github.com/myronsi/messenger-back/internal/config"
)

// StorageFrom builds the storage the configuration names. ping is nil for the disk backend, which needs no
// readiness check.
func StorageFrom(c config.Media) (s Storage, ping func(context.Context) error, err error) {
	if c.Backend == "s3" {
		st, err := NewS3(S3Options{
			Endpoint: c.S3Endpoint, AccessKey: c.S3AccessKey.Reveal(), SecretKey: c.S3SecretKey.Reveal(),
			Bucket: c.S3Bucket, Region: c.S3Region, PublicEndpoint: c.S3PublicEndpoint,
		})
		if err != nil {
			return nil, nil, err
		}
		return st, st.Ping, nil
	}
	d, err := NewDisk(c.Dir)
	return d, nil, err
}
