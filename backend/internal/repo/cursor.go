package repo

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// encodeCursor/decodeCursor 实现 keyset 分页的游标——不是 offset（决策
// 53：深分页在契约层面就不可表达）。游标只编码"最后一行的排序键"
// (created_at, id)，不透出任何 SQL 细节。
func encodeCursor(k cursorKey) string {
	raw := k.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(k.ID, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(s string) (cursorKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursorKey{}, err
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return cursorKey{}, fmt.Errorf("格式不对：%q", string(raw))
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return cursorKey{}, err
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return cursorKey{}, err
	}
	return cursorKey{CreatedAt: t, ID: id}, nil
}
