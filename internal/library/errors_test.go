package library

import (
	"errors"
	"fmt"
	"io/fs"
	"testing"
)

func TestIsPermissionError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "fs permission", err: fs.ErrPermission, want: true},
		{name: "wrapped fs permission", err: fmt.Errorf("wrapped: %w", fs.ErrPermission), want: true},
		{name: "windows string", err: errors.New("open file: Access is denied"), want: true},
		{name: "linux string", err: errors.New("mkdir /audiobooks: permission denied"), want: true},
		{name: "other", err: errors.New("network timeout"), want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPermissionError(tc.err); got != tc.want {
				t.Fatalf("isPermissionError() = %v, want %v", got, tc.want)
			}
		})
	}
}
