package oshsetup

import (
	"strconv"
	"strings"
)

// OpenShell's gateway.toml, edited by line: the few keys the setup sets
// (CONTRACT.md §16a). Everything else in the file stays as it is,
// comments and order included. Only plain "key = value" lines in plain
// [table] sections are read; that is all OpenShell's own files have where
// these keys live.

// tomlString is a TOML basic string.
func tomlString(s string) string { return strconv.Quote(s) }

// tableOf says which table a line opens ("" when it opens none).
func tableOf(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if strings.HasPrefix(t, "[") && !strings.HasPrefix(t, "[[") && strings.HasSuffix(t, "]") {
		return strings.TrimSpace(t[1 : len(t)-1]), true
	}
	return "", false
}

// keyOf says which key a line sets ("" when it sets none).
func keyOf(line string) string {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") {
		return ""
	}
	k, _, ok := strings.Cut(t, "=")
	if !ok {
		return ""
	}
	return strings.TrimSpace(k)
}

// getTOML is the raw value of key in table, and whether it is set.
func getTOML(text, table, key string) (string, bool) {
	cur := ""
	for _, line := range strings.Split(text, "\n") {
		if t, ok := tableOf(line); ok {
			cur = t
			continue
		}
		if cur == table && keyOf(line) == key {
			_, v, _ := strings.Cut(line, "=")
			v = strings.TrimSpace(v)
			if i := strings.Index(v, " #"); i >= 0 && !strings.HasPrefix(v, `"`) {
				v = strings.TrimSpace(v[:i])
			}
			return v, true
		}
	}
	return "", false
}

// setTOML sets key in table to the raw value: the line that sets it is
// replaced, or one is added at the end of the table, or the table is
// added at the end of the file.
func setTOML(text, table, key, value string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	set := key + " = " + value
	cur, last := "", -1
	for i, line := range lines {
		if t, ok := tableOf(line); ok {
			cur = t
			if t == table {
				last = i
			}
			continue
		}
		if cur != table {
			continue
		}
		if keyOf(line) == key {
			lines[i] = set
			return strings.Join(lines, "\n") + "\n"
		}
		if strings.TrimSpace(line) != "" {
			last = i
		}
	}
	if last < 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "["+table+"]", set)
		return strings.Join(lines, "\n") + "\n"
	}
	lines = append(lines[:last+1], append([]string{set}, lines[last+1:]...)...)
	return strings.Join(lines, "\n") + "\n"
}

// unsetTOML removes the line that sets key in table, if there is one.
func unsetTOML(text, table, key string) string {
	lines := strings.Split(text, "\n")
	out := lines[:0]
	cur := ""
	for _, line := range lines {
		if t, ok := tableOf(line); ok {
			cur = t
		} else if cur == table && keyOf(line) == key {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
