package setup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectories(t *testing.T) {
	origAGHWork := DirAGHWork
	origAGHConf := DirAGHConf
	origUnbound := DirUnbound
	origLego := DirLego
	t.Cleanup(func() {
		DirAGHWork = origAGHWork
		DirAGHConf = origAGHConf
		DirUnbound = origUnbound
		DirLego = origLego
	})

	tempDir := t.TempDir()
	DirAGHWork = filepath.Join(tempDir, "work")
	DirAGHConf = filepath.Join(tempDir, "conf")
	DirUnbound = filepath.Join(tempDir, "unbound")
	DirLego = filepath.Join(tempDir, "lego")

	t.Run("Success", func(t *testing.T) {
		if err := Directories(); err != nil {
			t.Fatalf("Directories() error = %v", err)
		}
		for _, d := range []string{DirAGHWork, DirAGHConf, DirUnbound, DirLego} {
			if info, err := os.Stat(d); err != nil || !info.IsDir() {
				t.Errorf("expected directory %s to exist", d)
			}
		}
	})

	t.Run("BlockedPathFailure", func(t *testing.T) {
		blocked := filepath.Join(tempDir, "blocked_dir")
		if err := os.WriteFile(blocked, []byte("file"), 0o600); err != nil {
			t.Fatalf("failed to create blocker file: %v", err)
		}
		DirAGHWork = filepath.Join(blocked, "child")
		if err := Directories(); err == nil {
			t.Error("expected error when directory creation is blocked")
		}
	})
}

func TestUnboundConfig(t *testing.T) {
	origConf := UnboundConfFile
	origDefault := UnboundDefaultConf
	t.Cleanup(func() {
		UnboundConfFile = origConf
		UnboundDefaultConf = origDefault
	})

	tempDir := t.TempDir()
	UnboundConfFile = filepath.Join(tempDir, "unbound.conf")
	UnboundDefaultConf = filepath.Join(tempDir, "default.conf")

	t.Run("MissingDefaultReturnsError", func(t *testing.T) {
		if err := UnboundConfig(); err == nil {
			t.Error("expected error when default config missing")
		}
	})

	t.Run("CopiesFromDefaultAndNormalizes", func(t *testing.T) {
		content := "server:\n  port: 1053\n  interface: 0.0.0.0\n"
		if err := os.WriteFile(UnboundDefaultConf, []byte(content), 0o600); err != nil {
			t.Fatalf("failed to write default: %v", err)
		}

		if err := UnboundConfig(); err != nil {
			t.Fatalf("UnboundConfig() error = %v", err)
		}

		data, err := os.ReadFile(filepath.Clean(UnboundConfFile))
		if err != nil {
			t.Fatalf("failed to read created config: %v", err)
		}
		res := string(data)
		if !strings.Contains(res, "    port: 5053") || !strings.Contains(res, "    interface: 127.0.0.1") {
			t.Errorf("config not normalized properly: %s", res)
		}
	})

	t.Run("NormalizesExistingConfig", func(t *testing.T) {
		content := "server:\n  port: 8053\n  interface: 10.0.0.1\n"
		if err := os.WriteFile(UnboundConfFile, []byte(content), 0o600); err != nil {
			t.Fatalf("failed to write existing config: %v", err)
		}

		if err := UnboundConfig(); err != nil {
			t.Fatalf("UnboundConfig() error = %v", err)
		}

		data, err := os.ReadFile(filepath.Clean(UnboundConfFile))
		if err != nil {
			t.Fatalf("failed to read updated config: %v", err)
		}
		res := string(data)
		if !strings.Contains(res, "    port: 5053") || !strings.Contains(res, "    interface: 127.0.0.1") {
			t.Errorf("config not normalized properly: %s", res)
		}
	})
}

func TestNormalizeUnboundConfig_OrphanCleanup(t *testing.T) {
	origConf := UnboundConfFile
	t.Cleanup(func() {
		UnboundConfFile = origConf
	})

	tempDir := t.TempDir()
	UnboundConfFile = filepath.Join(tempDir, "unbound.conf")

	orphan := filepath.Join(tempDir, "unbound.conf.12345.tmp")
	if err := os.WriteFile(orphan, []byte("stale"), 0o600); err != nil {
		t.Fatalf("failed to create orphan tmp: %v", err)
	}
	unremovableDir := filepath.Join(tempDir, "unbound.conf.unremovable.tmp")
	if err := os.Mkdir(unremovableDir, 0o700); err != nil {
		t.Fatalf("failed to create unremovable tmp dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(unremovableDir, "nested"), []byte("data"), 0o600); err != nil {
		t.Fatalf("failed to write nested file: %v", err)
	}
	if err := os.WriteFile(UnboundConfFile, []byte("server:\n"), 0o600); err != nil {
		t.Fatalf("failed to write conf: %v", err)
	}

	if err := normalizeUnboundConfig(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Error("expected orphan temp file to be deleted")
	}
}

func TestNormalizeUnboundConfig_Basics(t *testing.T) {
	origConf := UnboundConfFile
	t.Cleanup(func() {
		UnboundConfFile = origConf
	})

	tempDir := t.TempDir()
	UnboundConfFile = filepath.Join(tempDir, "unbound.conf")

	t.Run("MissingConfigFileError", func(t *testing.T) {
		UnboundConfFile = filepath.Join(tempDir, "missing.conf")
		if err := normalizeUnboundConfig(); err == nil {
			t.Error("expected error for missing config file")
		}
	})

	t.Run("RenameTargetIsDirectoryFails", func(t *testing.T) {
		confFile := filepath.Join(tempDir, "rename_target.conf")
		if err := os.WriteFile(confFile, []byte("server:\n"), 0o600); err != nil {
			t.Fatalf("failed to write conf: %v", err)
		}
		UnboundConfFile = confFile

		origClose := closeFile
		t.Cleanup(func() { closeFile = origClose })
		closeFile = func(f *os.File) error {
			if err := f.Close(); err != nil {
				t.Logf("test close failed: %v", err)
			}
			if err := os.Remove(confFile); err != nil {
				t.Logf("remove conf failed: %v", err)
			}
			if err := os.Mkdir(confFile, 0o700); err != nil {
				t.Logf("mkdir dir failed: %v", err)
			}
			if err := os.WriteFile(filepath.Join(confFile, "child"), []byte("data"), 0o600); err != nil {
				t.Logf("write child failed: %v", err)
			}
			return nil
		}

		if err := normalizeUnboundConfig(); err == nil {
			t.Error("expected error when rename target is a non-empty directory")
		}
	})

	t.Run("GlobPatternError", func(t *testing.T) {
		badDir := filepath.Join(tempDir, "bad[pattern")
		if err := os.Mkdir(badDir, 0o700); err != nil {
			t.Fatalf("failed to create dir: %v", err)
		}
		conf := filepath.Join(badDir, "unbound.conf")
		if err := os.WriteFile(conf, []byte("server:\n"), 0o600); err != nil {
			t.Fatalf("failed to write conf: %v", err)
		}
		UnboundConfFile = conf
		if err := normalizeUnboundConfig(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestNormalizeUnboundConfig_TempFileErrors(t *testing.T) {
	origConf := UnboundConfFile
	t.Cleanup(func() {
		UnboundConfFile = origConf
	})

	tempDir := t.TempDir()
	UnboundConfFile = filepath.Join(tempDir, "unbound.conf")

	t.Run("CreateTempFailsOnReadOnlyDir", func(t *testing.T) {
		roDir := filepath.Join(tempDir, "readonly_dir")
		if err := os.Mkdir(roDir, 0o700); err != nil {
			t.Fatalf("failed to create dir: %v", err)
		}
		conf := filepath.Join(roDir, "unbound.conf")
		if err := os.WriteFile(conf, []byte("server:\n"), 0o600); err != nil {
			t.Fatalf("failed to write conf: %v", err)
		}
		// #nosec G302 - Directory permissions restricted to read-only for failure testing.
		if err := os.Chmod(roDir, 0o500); err != nil {
			t.Fatalf("failed to chmod dir: %v", err)
		}
		t.Cleanup(func() {
			// #nosec G302 - Directory permissions restored for cleanup.
			if err := os.Chmod(roDir, 0o700); err != nil {
				t.Logf("cleanup chmod failed: %v", err)
			}
		})
		UnboundConfFile = conf
		if err := normalizeUnboundConfig(); err == nil {
			t.Error("expected error when temp file creation fails")
		}
	})

	t.Run("DeferRemoveErrorLogged", func(t *testing.T) {
		subDir := filepath.Join(tempDir, "unremovable_sub")
		if err := os.Mkdir(subDir, 0o700); err != nil {
			t.Fatalf("failed to create dir: %v", err)
		}
		confFile := filepath.Join(subDir, "unbound.conf")
		if err := os.WriteFile(confFile, []byte("server:\n"), 0o600); err != nil {
			t.Fatalf("failed to write conf: %v", err)
		}
		UnboundConfFile = confFile

		origClose := closeFile
		t.Cleanup(func() {
			closeFile = origClose
			// #nosec G302 - Directory permissions restored for cleanup.
			if err := os.Chmod(subDir, 0o700); err != nil {
				t.Logf("cleanup chmod failed: %v", err)
			}
		})
		closeFile = func(f *os.File) error {
			if err := f.Close(); err != nil {
				t.Logf("test close failed: %v", err)
			}
			if err := os.Remove(confFile); err != nil {
				t.Logf("remove conf failed: %v", err)
			}
			if err := os.Mkdir(confFile, 0o700); err != nil {
				t.Logf("mkdir conflict failed: %v", err)
			}
			if err := os.WriteFile(filepath.Join(confFile, "child"), []byte("data"), 0o600); err != nil {
				t.Logf("write child failed: %v", err)
			}
			// #nosec G302 - Directory permissions restricted for failure path test.
			if err := os.Chmod(subDir, 0o500); err != nil {
				t.Logf("test chmod failed: %v", err)
			}
			return nil
		}

		if err := normalizeUnboundConfig(); err == nil {
			t.Error("expected rename failure")
		}
	})
}

func TestNormalizeUnboundConfig_WriteSyncCloseErrors(t *testing.T) {
	origConf := UnboundConfFile
	t.Cleanup(func() {
		UnboundConfFile = origConf
	})

	tempDir := t.TempDir()
	cases := []struct {
		setup func(t *testing.T)
		name  string
	}{
		{
			name: "WriteStringError",
			setup: func(t *testing.T) {
				origWrite := writeString
				t.Cleanup(func() { writeString = origWrite })
				writeString = func(*os.File, string) (int, error) {
					return 0, errors.New("write failed")
				}
			},
		},
		{
			name: "SyncFileError",
			setup: func(t *testing.T) {
				origSync := syncFile
				t.Cleanup(func() { syncFile = origSync })
				syncFile = func(*os.File) error {
					return errors.New("sync failed")
				}
			},
		},
		{
			name: "CloseFileError",
			setup: func(t *testing.T) {
				origClose := closeFile
				t.Cleanup(func() { closeFile = origClose })
				closeFile = func(*os.File) error {
					return errors.New("close failed")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conf := filepath.Join(tempDir, tc.name+".conf")
			if err := os.WriteFile(conf, []byte("server:\n"), 0o600); err != nil {
				t.Fatalf("failed to write conf: %v", err)
			}
			UnboundConfFile = conf
			tc.setup(t)
			if err := normalizeUnboundConfig(); err == nil {
				t.Errorf("expected error for %s", tc.name)
			}
		})
	}
}

func TestTrustAnchor(t *testing.T) {
	origDir := DirUnbound
	origRun := runAnchor
	t.Cleanup(func() {
		DirUnbound = origDir
		runAnchor = origRun
	})

	tempDir := t.TempDir()
	DirUnbound = tempDir
	keyPath := filepath.Join(tempDir, "root.key")

	t.Run("DefaultAnchorFunctionCoverage", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := defaultRunAnchor(ctx); err != nil {
			t.Logf("expected anchor error on cancelled context: %v", err)
		}
	})

	t.Run("DefaultAnchorSuccess", func(t *testing.T) {
		origCmd := execCommand
		t.Cleanup(func() { execCommand = origCmd })
		execCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "true")
		}
		if err := defaultRunAnchor(t.Context()); err != nil {
			t.Errorf("expected nil error on successful command, got %v", err)
		}
	})

	t.Run("KeyAlreadyExists", func(t *testing.T) {
		if err := os.WriteFile(keyPath, []byte(". IN DS 20326 8 2 E06D44B80B8F1D39A95C0B0D7C65D08458E880409BBC683457104237E7F08387"), 0o600); err != nil {
			t.Fatalf("failed to write root key: %v", err)
		}
		if err := TrustAnchor(); err != nil {
			t.Errorf("expected nil when key exists, got %v", err)
		}
	})

	t.Run("BootstrapCreatesValidKey", func(t *testing.T) {
		if err := os.Remove(keyPath); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove key failed: %v", err)
		}
		runAnchor = func(_ context.Context) error {
			return os.WriteFile(keyPath, []byte("valid-anchor-content"), 0o600)
		}

		if err := TrustAnchor(); err != nil {
			t.Errorf("expected success on bootstrap, got %v", err)
		}
	})

	t.Run("BootstrapCreatesEmptyFile", func(t *testing.T) {
		if err := os.Remove(keyPath); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove key failed: %v", err)
		}
		runAnchor = func(_ context.Context) error {
			return os.WriteFile(keyPath, []byte(""), 0o600)
		}

		if err := TrustAnchor(); err == nil {
			t.Error("expected error on empty anchor file")
		}
	})

	t.Run("BootstrapFailsToCreateFile", func(t *testing.T) {
		if err := os.Remove(keyPath); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove key failed: %v", err)
		}
		runAnchor = func(_ context.Context) error {
			return errors.New("command failed")
		}

		if err := TrustAnchor(); err == nil {
			t.Error("expected error when file was not created")
		}
	})
}

func TestCopyFile(t *testing.T) {
	tempDir := t.TempDir()
	src := filepath.Join(tempDir, "source.conf")
	dst := filepath.Join(tempDir, "dest.conf")

	t.Run("Success", func(t *testing.T) {
		if err := os.WriteFile(src, []byte("server: verbosity: 1"), 0o600); err != nil {
			t.Fatalf("Failed to setup source file: %v", err)
		}

		if err := copyFile(src, dst); err != nil {
			t.Fatalf("Expected successful copy, got: %v", err)
		}

		data, err := os.ReadFile(filepath.Clean(dst))
		if err != nil || string(data) != "server: verbosity: 1" {
			t.Errorf("Copied data mismatch or read error")
		}
	})

	t.Run("SourceMissing", func(t *testing.T) {
		if err := copyFile(filepath.Join(tempDir, "missing.conf"), filepath.Join(tempDir, "dst2.conf")); err == nil {
			t.Error("expected error on missing source")
		}
	})

	t.Run("DestinationAlreadyExistsWithExclusive", func(t *testing.T) {
		if err := copyFile(src, dst); err == nil {
			t.Error("expected error when destination already exists")
		}
	})

	t.Run("CopyErrorsTable", func(t *testing.T) {
		cases := []struct {
			setup func(t *testing.T) string
			name  string
		}{
			{
				name: "SourceIsDirectoryFailsCopy",
				setup: func(_ *testing.T) string {
					srcDir := filepath.Join(tempDir, "source_dir")
					if err := os.Mkdir(srcDir, 0o700); err != nil {
						t.Fatalf("failed to create src dir: %v", err)
					}
					return srcDir
				},
			},
			{
				name: "SyncError",
				setup: func(t *testing.T) string {
					origSync := syncFile
					t.Cleanup(func() { syncFile = origSync })
					syncFile = func(*os.File) error {
						return errors.New("sync error")
					}
					return src
				},
			},
			{
				name: "DestCloseError",
				setup: func(t *testing.T) string {
					origClose := closeFile
					t.Cleanup(func() { closeFile = origClose })
					closeFile = func(f *os.File) error {
						if err := f.Close(); err != nil {
							t.Logf("test close failed: %v", err)
						}
						if strings.Contains(f.Name(), "dst_err") {
							return errors.New("dest close error")
						}
						return nil
					}
					return src
				},
			},
			{
				name: "SourceCloseError",
				setup: func(t *testing.T) string {
					origClose := closeFile
					t.Cleanup(func() { closeFile = origClose })
					closeFile = func(f *os.File) error {
						if err := f.Close(); err != nil {
							t.Logf("test close failed: %v", err)
						}
						if strings.Contains(f.Name(), "source.conf") {
							return errors.New("source close error")
						}
						return nil
					}
					return src
				},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				customSrc := tc.setup(t)
				targetDst := filepath.Join(tempDir, "dst_err_"+tc.name+".conf")
				if err := copyFile(customSrc, targetDst); err == nil {
					t.Errorf("expected error for %s", tc.name)
				}
			})
		}
	})
}

func TestDefaultFileOperations_ErrorPaths(t *testing.T) {
	tempDir := t.TempDir()
	f, err := os.Create(filepath.Clean(filepath.Join(tempDir, "closed.tmp")))
	if err != nil {
		t.Fatalf("failed to create file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("failed to close file: %v", err)
	}

	if err := defaultSyncFile(f); err == nil {
		t.Error("expected sync error on closed file")
	}
	if err := defaultCloseFile(f); err == nil {
		t.Error("expected close error on already closed file")
	}
	if _, err := defaultWriteString(f, "data"); err == nil {
		t.Error("expected write error on closed file")
	}
}
