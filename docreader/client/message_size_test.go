package client

import "testing"

func TestMaxMessageSizeTransportOverride(t *testing.T) {
	for _, tc := range []struct {
		name, transport, upload string
		wantMB                  int
	}{
		{"independent transport overrides upload", "256", "50", 256},
		{"unset transport uses upload", "", "100", 100},
		{"invalid transport uses upload", "invalid", "100", 100},
		{"nonpositive transport uses upload", "0", "100", 100},
		{"negative transport uses upload", "-1", "100", 100},
		{"unset limits keep legacy default", "", "", 50},
		{"invalid limits keep legacy default", "invalid", "-1", 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DOCREADER_GRPC_MAX_FILE_SIZE_MB", tc.transport)
			t.Setenv("MAX_FILE_SIZE_MB", tc.upload)
			if got := getMaxMessageSize(); got != tc.wantMB*1024*1024 {
				t.Fatalf("message size = %d bytes, want %d MiB", got, tc.wantMB)
			}
		})
	}
}
