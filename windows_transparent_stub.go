//go:build !windows

package main

func runWindowsTransparentMode(_ []*playerState, _ apiConfig) bool {
	return false
}
