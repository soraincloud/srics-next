package library

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"strconv"
	"strings"
)

const imageSequenceSetting = "image-name-sequence"

func imageNameNumber(name string) (int64, bool) {
	name = strings.TrimSuffix(name, path.Ext(name))
	if !strings.HasPrefix(name, "IMG-") {
		return 0, false
	}
	number, err := strconv.ParseInt(strings.TrimPrefix(name, "IMG-"), 10, 64)
	return number, err == nil && number > 0 && name == fmt.Sprintf("IMG-%06d", number)
}

func (l *Library) nextImageName() (string, int64, error) {
	var stored []byte
	err := l.db.QueryRow("SELECT value FROM settings WHERE key=?", imageSequenceSetting).Scan(&stored)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", 0, err
	}
	var last int64
	if !errors.Is(err, sql.ErrNoRows) {
		last, err = strconv.ParseInt(string(stored), 10, 64)
		if err != nil || last < 0 {
			return "", 0, errors.New("图片编号记录损坏，已停止上传，请检查资料索引")
		}
	} else {
		// Existing imports may already use this naming scheme. Initialize once
		// above their numbers, including trash and unfinished uploads.
		rows, e := l.db.Query(`SELECT name FROM items WHERE module='images'
UNION ALL SELECT json_extract(data,'$.name') FROM uploads WHERE json_extract(data,'$.module')='images'
UNION ALL SELECT json_extract(p.value,'$.name') FROM items i,json_each(i.pages) p WHERE i.module='images'`)
		if e != nil {
			return "", 0, e
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if e = rows.Scan(&name); e != nil {
				return "", 0, e
			}
			if number, ok := imageNameNumber(name); ok && number > last {
				last = number
			}
		}
		if e = rows.Err(); e != nil {
			return "", 0, e
		}
	}
	if last == math.MaxInt64 {
		return "", 0, errors.New("图片编号已达到上限")
	}
	return fmt.Sprintf("IMG-%06d", last+1), last + 1, nil
}

// Reserve the number and register the task together. Retrying a registration
// uses the stored task's number; cancelling/deleting never reuses a number.
func (l *Library) saveNumberedImageUpload(up Upload, number int64) error {
	data, err := json.Marshal(up)
	if err != nil {
		return err
	}
	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", imageSequenceSetting, []byte(strconv.FormatInt(number, 10))); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO uploads(id,data) VALUES(?,?)", up.ID, data); err != nil {
		return err
	}
	return tx.Commit()
}

func imageNameExtension(kind string) string {
	switch kind {
	case "image/webp":
		return ".webp"
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	}
	return ""
}
