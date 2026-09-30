package main

import (
	"fmt"
	"strings"
)

func placeholders(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", count), ",")
}

func sqlInQuery(query string, ids []int64) (string, []any) {
	args := make([]any, len(ids))
	for index, id := range ids {
		args[index] = id
	}
	return fmt.Sprintf(query, placeholders(len(ids))), args
}
