package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidArch(t *testing.T) {
	assert.True(t, validArch("amd64"))
	assert.True(t, validArch("arm64"))
	assert.False(t, validArch("x86_64"), "must be the Go/goreleaser name, not the uname one")
	assert.False(t, validArch(""))
	assert.False(t, validArch("AMD64"), "case-sensitive")
}

func TestValidateArchFlag(t *testing.T) {
	require.NoError(t, validateArchFlag("amd64"))
	require.NoError(t, validateArchFlag("arm64"))
	err := validateArchFlag("mips")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `invalid --arch "mips"`)
}

func TestMapUnameArch(t *testing.T) {
	got, err := mapUnameArch("x86_64")
	require.NoError(t, err)
	assert.Equal(t, "amd64", got)

	got, err = mapUnameArch("aarch64")
	require.NoError(t, err)
	assert.Equal(t, "arm64", got)

	_, err = mapUnameArch("riscv64")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unsupported host architecture "riscv64"`)

	_, err = mapUnameArch("")
	require.Error(t, err)
}
