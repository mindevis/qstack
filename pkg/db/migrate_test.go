package db

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestValidateMigrations(t *testing.T) {
	up := "-- +goose Up\nCREATE TABLE t (id int);\n"
	tests := []struct {
		name    string
		files   fstest.MapFS
		wantErr string // empty = valid
	}{
		{"valid", fstest.MapFS{"0001_a.sql": {Data: []byte(up)}, "0002_b.sql": {Data: []byte(up)}}, ""},
		{"empty down section is fine", fstest.MapFS{"0001_a.sql": {Data: []byte(up + "-- +goose Down\n-- nothing to undo\n")}}, ""},
		{"down with statements", fstest.MapFS{"0001_a.sql": {Data: []byte(up + "-- +goose Down\nDROP TABLE t;\n")}}, "forward-only"},
		{"missing up", fstest.MapFS{"0001_a.sql": {Data: []byte("CREATE TABLE t (id int);\n")}}, "missing"},
		{"bad name", fstest.MapFS{"1_a.sql": {Data: []byte(up)}}, "NNNN_name.sql"},
		{"uppercase name", fstest.MapFS{"0001_Create.sql": {Data: []byte(up)}}, "NNNN_name.sql"},
		{"gap", fstest.MapFS{"0001_a.sql": {Data: []byte(up)}, "0003_c.sql": {Data: []byte(up)}}, "consecutive"},
		{"not starting at 1", fstest.MapFS{"0002_b.sql": {Data: []byte(up)}}, "consecutive"},
		{"empty", fstest.MapFS{}, "no migrations"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateMigrations(tt.files)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("err = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}
