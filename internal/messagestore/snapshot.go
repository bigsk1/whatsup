package messagestore

import (
	"context"
	"database/sql"
)

type SnapshotInfo struct {
	Scopes                []string
	Messages, Attachments uint64
}

func (d *DB) SnapshotInfo() (result SnapshotInfo, err error) {
	err = d.transaction(func(ctx context.Context, tx *sql.Tx) error {
		result.Scopes = append([]string{}, d.manifest.Contacts...)
		for _, id := range result.Scopes {
			m, _, err := d.loadMeta(ctx, tx, id)
			if err != nil {
				return err
			}
			result.Messages += m.Rows
			result.Attachments += m.AttachmentRows
		}
		return nil
	})
	return
}
