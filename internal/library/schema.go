package library

import (
	"database/sql"
	"errors"
)

// Check the schema declared by user_version before migration or normal use.
// SQLite quick_check alone accepts a structurally valid but incomplete schema.
func checkSchema(db *sql.DB, version int) error {
	queries := []struct {
		version int
		query   string
	}{
		{1, "SELECT key,value FROM settings LIMIT 0"},
		{1, "SELECT id,data FROM uploads LIMIT 0"},
		{1, "SELECT seq,id,module,name,tags,created,deleted,revision,pages,progress FROM items LIMIT 0"},
		{2, "SELECT id,novel_id,title,body,position,revision,deleted,updated FROM chapters LIMIT 0"},
		{2, "SELECT chapter_id,revision,title,body,saved,automatic FROM chapter_versions LIMIT 0"},
		{2, "SELECT novel_id,chapter_id FROM novel_reading LIMIT 0"},
		{3, "SELECT seq,id,payload,object,thumb,hash,thumb_hash FROM private_items LIMIT 0"},
		{4, "SELECT id,private,payload FROM transfers LIMIT 0"},
		{4, "SELECT transfer_id,idx,object,hash FROM transfer_chunks LIMIT 0"},
		{7, "SELECT novel_id,completed,paragraph,fraction,reading_revision,reading_updated FROM novel_state LIMIT 0"},
	}
	for _, q := range queries {
		if q.version > version {
			continue
		}
		rows, err := db.Query(q.query)
		if err != nil {
			return errors.New("资料索引结构不完整，已停止；请保留目录并从备份恢复")
		}
		if err = rows.Close(); err != nil {
			return err
		}
	}
	return nil
}
