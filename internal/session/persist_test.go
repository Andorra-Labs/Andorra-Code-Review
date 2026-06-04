package session

import (
	"runtime"
	"testing"
)

func TestEncodeRepoPath(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "empty",
		},
		{
			name:     "relative path",
			input:    "relative/path/to/repo",
			expected: "relative-path-to-repo",
		},
		{
			name:     "path with mixed separators",
			input:    "path/to\\mixed",
			expected: "path-to-mixed",
		},
	}

	// Add platform-specific test cases
	if runtime.GOOS == "windows" {
		tests = append(tests, []struct {
			name     string
			input    string
			expected string
		}{
			{
				name:     "windows drive path",
				input:    "D:\\Users\\admin\\project",
				expected: "D_Users-admin-project",
			},
			{
				name:     "windows C drive",
				input:    "C:\\code\\myapp",
				expected: "C_code-myapp",
			},
			{
				name:     "windows relative path",
				input:    "relative\\path\\to\\repo",
				expected: "relative-path-to-repo",
			},
			{
				name:     "windows drive only",
				input:    "C:",
				expected: "C_",
			},
			{
				name:     "windows drive with separator only",
				input:    "D:\\",
				expected: "D_",
			},
		}...)
	} else {
		tests = append(tests, []struct {
			name     string
			input    string
			expected string
		}{
			{
				name:     "unix absolute path",
				input:    "/home/user/project",
				expected: "home-user-project",
			},
			{
				name:     "unix nested path",
				input:    "/Users/john/dev/myapp",
				expected: "Users-john-dev-myapp",
			},
			{
				name:     "unix root only",
				input:    "/",
				expected: "empty",
			},
		}...)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := encodeRepoPath(tt.input)
			if result != tt.expected {
				t.Errorf("encodeRepoPath(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
