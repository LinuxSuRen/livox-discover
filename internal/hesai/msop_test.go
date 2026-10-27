package hesai

import "testing"

func TestIsMSOP(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"JT128-style packet", append([]byte{0xEE, 0xFF, 0x01, 0x04}, make([]byte, 1096)...), true},
		{"minimal marker", []byte{0xEE, 0xFF}, true},
		{"velodyne order is not hesai", []byte{0xFF, 0xEE, 0x01, 0x02}, false},
		{"single byte", []byte{0xEE}, false},
		{"empty", nil, false},
		{"unrelated", []byte("hello point cloud"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsMSOP(tt.data); got != tt.want {
				t.Errorf("IsMSOP(% x...) = %v, want %v", tt.data[:min(2, len(tt.data))], got, tt.want)
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
