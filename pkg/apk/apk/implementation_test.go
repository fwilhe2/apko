// Copyright 2023 Chainguard, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package apk

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"chainguard.dev/apko/pkg/apk/auth"
	apkfs "chainguard.dev/apko/pkg/apk/fs"
)

const (
	testDemoKey = `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAwXEJ8uVwJPODshTkf2BH
pH5fVVDppOa974+IQJsZDmGd3Ny0dcd+WwYUhNFUW3bAfc3/egaMWCaprfaHn+oS
4ddbOFgbX8JCHdru/QMAAU0aEWSMybfJGA569c38fNUF/puX6XK/y0lD2SS3YQ/a
oJ5jb5eNrQGR1HHMAd0G9WC4JeZ6WkVTkrcOw55F00aUPGEjejreXBerhTyFdabo
dSfc1TILWIYD742Lkm82UBOPsOSdSfOdsMOOkSXxhdCJuCQQ70DHkw7Epy9r+X33
ybI4r1cARcV75OviyhD8CFhAlapLKaYnRFqFxlA515e6h8i8ih/v3MSEW17cCK0b
QwIDAQAB
-----END PUBLIC KEY-----
`
)

var (
	testPkg = Package{
		Name:    "alpine-baselayout",
		Version: "3.2.0-r23",
		Arch:    testArch,
		// This was generated by converting the hex of 2cbab6a8336b4bfa919e1c50de1b18fec1db4277.ctl.tar.gz to a byte slice.
		// If for whatever reason the control checksum changes, you will need to update this as well, and the tests will log
		// the files it _did_ find instead of what it expected in a given directory, so you can bump this.
		// But it shouldn't just change unless you change the test data!
		Checksum: []byte{44, 186, 182, 168, 51, 107, 75, 250, 145, 158, 28, 80, 222, 27, 24, 254, 193, 219, 66, 119},
	}
	testPkgFilename    = fmt.Sprintf("%s-%s.apk", testPkg.Name, testPkg.Version)
	testUser, testPass = "user", "pass"
)

func TestInitDB(t *testing.T) {
	src := apkfs.NewMemFS()
	apk, err := New(WithFS(src), WithIgnoreMknodErrors(ignoreMknodErrors))
	require.NoError(t, err)
	err = apk.InitDB(context.Background())
	require.NoError(t, err)
	// check all of the contents
	for _, d := range initDirectories {
		fi, err := fs.Stat(src, d.path)
		require.NoError(t, err, "error statting %s", d.path)
		require.True(t, fi.IsDir(), "expected %s to be a directory, got %v", d.path, fi.Mode())
		require.Equal(t, d.perms, fi.Mode().Perm(), "expected %s to have permissions %v, got %v", d.path, d.perms, fi.Mode().Perm())
	}
	for _, f := range initFiles {
		fi, err := fs.Stat(src, f.path)
		require.NoError(t, err, "error statting %s", f.path)
		require.True(t, fi.Mode().IsRegular(), "expected %s to be a regular file, got %v", f.path, fi.Mode())
		require.Equal(t, f.perms, fi.Mode().Perm(), "mismatched permissions for %s", f.path)
		require.GreaterOrEqual(t, fi.Size(), int64(len(f.contents)), "mismatched size for %s", f.path) // actual file can be bigger than original size
	}
	if !ignoreMknodErrors {
		for _, f := range initDeviceFiles {
			fi, err := fs.Stat(src, f.path)
			require.NoError(t, err, "error statting %s", f.path)
			require.Equal(t, fi.Mode().Type()&os.ModeCharDevice, os.ModeCharDevice, "expected %s to be a character file, got %v", f.path, fi.Mode())
			targetPerms := f.perms
			actualPerms := fi.Mode().Perm()
			require.Equal(t, targetPerms, actualPerms, "expected %s to have permissions %v, got %v", f.path, targetPerms, actualPerms)
		}
	}
}

func TestSetWorld(t *testing.T) {
	ctx := context.Background()
	src := apkfs.NewMemFS()
	apk, err := New(WithFS(src), WithIgnoreMknodErrors(ignoreMknodErrors))
	require.NoError(t, err)
	// for initialization
	err = src.MkdirAll("etc/apk", 0o755)
	require.NoError(t, err)

	// set these packages in a random order; it should write them to world in the correct order
	packages := []string{"foo", "bar", "abc", "zulu"}
	err = apk.SetWorld(ctx, packages)
	require.NoError(t, err)

	// check all of the contents
	actual, err := src.ReadFile("etc/apk/world")
	require.NoError(t, err)

	sort.Strings(packages)
	expected := strings.Join(packages, "\n") + "\n"
	require.Equal(t, expected, string(actual), "unexpected content for etc/apk/world:\nexpected %s\nactual %s", expected, actual)
}

func TestSetWorldWithVersions(t *testing.T) {
	ctx := context.Background()
	src := apkfs.NewMemFS()
	apk, err := New(WithFS(src), WithIgnoreMknodErrors(ignoreMknodErrors))
	require.NoError(t, err)
	// for initialization
	err = src.MkdirAll("etc/apk", 0o755)
	require.NoError(t, err)

	// set these packages in a random order; it should write them to world in the correct order
	packages := []string{"foo=1.0.0", "bar=1.2.3", "abc", "zulu", "foo"}
	err = apk.SetWorld(ctx, packages)
	require.NoError(t, err)

	// check all of the contents
	actual, err := src.ReadFile("etc/apk/world")
	require.NoError(t, err)

	sort.Strings(packages)
	expected := strings.Join(packages, "\n") + "\n"
	require.Equal(t, expected, string(actual), "unexpected content for etc/apk/world:\nexpected %s\nactual %s", expected, actual)
}

func TestSetRepositories(t *testing.T) {
	ctx := context.Background()
	src := apkfs.NewMemFS()
	apk, err := New(WithFS(src), WithIgnoreMknodErrors(ignoreMknodErrors))
	require.NoError(t, err)
	// for initialization

	err = src.MkdirAll("etc/apk", 0o755)
	require.NoError(t, err)

	repos := []string{"https://dl-cdn.alpinelinux.org/alpine/v3.16/main", "https://dl-cdn.alpinelinux.org/alpine/v3.16/community"}
	err = apk.SetRepositories(ctx, repos)
	require.NoError(t, err)

	// check all of the contents
	actual, err := src.ReadFile("etc/apk/repositories")
	require.NoError(t, err)

	expected := strings.Join(repos, "\n") + "\n"
	require.Equal(t, expected, string(actual))
}

func TestSetRepositories_Empty(t *testing.T) {
	ctx := context.Background()
	src := apkfs.NewMemFS()
	apk, err := New(WithFS(src), WithIgnoreMknodErrors(ignoreMknodErrors))
	require.NoError(t, err)
	// for initialization

	err = src.MkdirAll("etc/apk", 0o755)
	require.NoError(t, err)

	repos := []string{}
	err = apk.SetRepositories(ctx, repos)
	require.Error(t, err)
}

func TestInitKeyring(t *testing.T) {
	src := apkfs.NewMemFS()
	a, err := New(WithFS(src), WithIgnoreMknodErrors(ignoreMknodErrors))
	require.NoError(t, err)

	dir, err := os.MkdirTemp("", "go-apk")
	require.NoError(t, err)

	keyPath := filepath.Join(dir, "alpine-devel@lists.alpinelinux.org-5e69ca50.rsa.pub")
	err = os.WriteFile(keyPath, []byte(testDemoKey), 0o644) //nolint:gosec
	require.NoError(t, err)

	// Add a local file and a remote key
	keyfiles := []string{
		keyPath, "https://alpinelinux.org/keys/alpine-devel%40lists.alpinelinux.org-4a6a0840.rsa.pub",
	}
	// ensure we send things from local
	a.SetClient(&http.Client{
		Transport: &testLocalTransport{root: testPrimaryPkgDir, basenameOnly: true},
	})

	require.NoError(t, a.InitKeyring(context.Background(), keyfiles, nil))
	// InitKeyring should have copied the local key and remote key to the right place
	fi, err := src.ReadDir(DefaultKeyRingPath)
	// should be no error reading them
	require.NoError(t, err)
	// should be 2 keys
	require.Len(t, fi, 2)

	// Add an invalid file
	keyfiles = []string{
		"/liksdjlksdjlksjlksjdl",
	}
	require.Error(t, a.InitKeyring(context.Background(), keyfiles, nil))

	// Add an invalid url
	keyfiles = []string{
		"http://sldkjflskdjflklksdlksdlkjslk.net",
	}
	require.Error(t, a.InitKeyring(context.Background(), keyfiles, nil))

	// add a remote key with HTTP Basic Auth
	keyfiles = []string{
		"https://user:pass@alpinelinux.org/keys/alpine-devel%40lists.alpinelinux.org-4a6a0840.rsa.pub",
	}
	a.SetClient(&http.Client{
		Transport: &testLocalTransport{root: testPrimaryPkgDir, basenameOnly: true, requireBasicAuth: true},
	})
	require.NoError(t, a.InitKeyring(context.Background(), keyfiles, nil))

	t.Run("auth", func(t *testing.T) {
		called := false
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			if gotuser, gotpass, ok := r.BasicAuth(); !ok || gotuser != testUser || gotpass != testPass {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			http.FileServer(http.Dir(testPrimaryPkgDir)).ServeHTTP(w, r)
		}))
		defer s.Close()
		host := strings.TrimPrefix(s.URL, "http://")

		ctx := context.Background()

		t.Run("good auth", func(t *testing.T) {
			src := apkfs.NewMemFS()
			err := src.MkdirAll("lib/apk/db", 0o755)
			require.NoError(t, err, "unable to mkdir /lib/apk/db")

			a, err := New(WithFS(src), WithAuthenticator(auth.StaticAuth(host, testUser, testPass)))
			require.NoError(t, err, "unable to create APK")
			err = a.InitDB(ctx)
			require.NoError(t, err)

			err = a.InitKeyring(ctx, []string{s.URL + "/alpine-devel@lists.alpinelinux.org-4a6a0840.rsa.pub"}, nil)
			require.NoErrorf(t, err, "unable to init keyring")
			require.True(t, called, "did not make request")
		})

		t.Run("bad auth", func(t *testing.T) {
			src := apkfs.NewMemFS()
			err := src.MkdirAll("lib/apk/db", 0o755)
			require.NoError(t, err, "unable to mkdir /lib/apk/db")

			a, err := New(WithFS(src), WithAuthenticator(auth.StaticAuth(host, "baduser", "badpass")))
			require.NoError(t, err, "unable to create APK")
			err = a.InitDB(ctx)
			require.NoError(t, err)

			err = a.InitKeyring(ctx, []string{s.URL + "/alpine-devel@lists.alpinelinux.org-4a6a0840.rsa.pub"}, nil)
			require.Error(t, err, "should fail with bad auth")
			require.True(t, called, "did not make request")
		})
	})
}

func TestLoadSystemKeyring(t *testing.T) {
	t.Run("non-existent dir", func(t *testing.T) {
		ctx := context.Background()
		src := apkfs.NewMemFS()
		a, err := New(WithFS(src), WithIgnoreMknodErrors(ignoreMknodErrors))
		require.NoError(t, err)

		// Read the empty dir, passing a non-existent location should err
		_, err = a.loadSystemKeyring(ctx, "/non/existent/dir")
		require.Error(t, err)
	})
	t.Run("empty dir", func(t *testing.T) {
		ctx := context.Background()
		src := apkfs.NewMemFS()
		a, err := New(WithFS(src), WithIgnoreMknodErrors(ignoreMknodErrors))
		require.NoError(t, err)

		// Read the empty dir, passing only one empty location should err
		emptyDir := "/var/test/keyring"
		err = src.MkdirAll(emptyDir, 0o755)
		require.NoError(t, err)
		_, err = a.loadSystemKeyring(ctx, emptyDir)
		require.Error(t, err)
	})
	tests := []struct {
		name  string
		paths []string
	}{
		{"non-standard dir", []string{"/var/test/keyring"}},
		{"standard dir", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			arch := ArchToAPK(runtime.GOARCH)
			src := apkfs.NewMemFS()
			a, err := New(WithFS(src), WithIgnoreMknodErrors(ignoreMknodErrors))
			require.NoError(t, err)

			// Write some dummy keyfiles in a random location
			targetDir := DefaultSystemKeyRingPath
			if len(tt.paths) > 0 {
				targetDir = tt.paths[0]
			}
			// make the base directory and the arch-specific directory
			err = src.MkdirAll(targetDir, 0o755)
			require.NoError(t, err)
			err = src.MkdirAll(filepath.Join(targetDir, arch), 0o755)
			require.NoError(t, err)
			for _, h := range []string{"4a6a0840", "5243ef4b", "5261cecb", "6165ee59", "61666e3f"} {
				require.NoError(t, src.WriteFile(
					filepath.Join(targetDir, fmt.Sprintf("alpine-devel@lists.alpinelinux.org-%s.rsa.pub", h)),
					[]byte("testABC"), os.FileMode(0o644),
				))
			}

			for _, h := range []string{"4a6a0840", "5243ef4b", "5261cecb", "6165ee59", "61666e3f"} {
				err := src.WriteFile(
					filepath.Join(targetDir, arch, fmt.Sprintf("alpine-devel@lists.alpinelinux.org-%s.rsa.pub", h)),
					[]byte("testABC"), os.FileMode(0o644),
				)
				require.NoError(t, err)
			}

			// Add a readme file to ensure we dont read it
			require.NoError(t, src.WriteFile(
				filepath.Join(targetDir, "README.txt"), []byte("testABC"), os.FileMode(0o644),
			))

			// Successful read
			keyFiles, err := a.loadSystemKeyring(ctx, tt.paths...)
			require.NoError(t, err)
			require.Len(t, keyFiles, 5)
			// should not take into account extraneous files
			require.NotContains(t, keyFiles, filepath.Join(targetDir, "README.txt"))
		})
	}
}

func TestFetchPackage(t *testing.T) {
	var (
		repo          = Repository{URI: fmt.Sprintf("%s/%s", testAlpineRepos, testArch)}
		packages      = []*Package{&testPkg}
		repoWithIndex = repo.WithIndex(&APKIndex{
			Packages: packages,
		})
		testEtag = "testetag"
		pkg      = NewRepositoryPackage(&testPkg, repoWithIndex)
		ctx      = context.Background()
	)
	prepLayout := func(t *testing.T, cache string) *APK {
		src := apkfs.NewMemFS()
		err := src.MkdirAll("lib/apk/db", 0o755)
		require.NoError(t, err, "unable to mkdir /lib/apk/db")

		opts := []Option{WithFS(src), WithIgnoreMknodErrors(ignoreMknodErrors)}
		if cache != "" {
			opts = append(opts, WithCache(cache, false))
		}
		a, err := New(opts...)
		require.NoError(t, err, "unable to create APK")
		err = a.InitDB(ctx)
		require.NoError(t, err)

		// set a client so we use local testdata instead of heading out to the Internet each time
		return a
	}
	t.Run("no cache", func(t *testing.T) {
		a := prepLayout(t, "")
		a.SetClient(&http.Client{
			Transport: &testLocalTransport{root: testPrimaryPkgDir, basenameOnly: true},
		})
		_, err := a.FetchPackage(ctx, pkg)
		require.NoErrorf(t, err, "unable to install package")
	})
	t.Run("cache miss no network", func(t *testing.T) {
		// we use a transport that always returns a 404 so we know we're not hitting the network
		// it should fail for a cache hit
		tmpDir := t.TempDir()
		a := prepLayout(t, tmpDir)
		a.SetClient(&http.Client{
			Transport: &testLocalTransport{fail: true},
		})
		_, err := a.FetchPackage(ctx, pkg)
		require.Error(t, err, "should fail when no cache and no network")
	})
	t.Run("cache miss network should fill cache", func(t *testing.T) {
		tmpDir := t.TempDir()
		a := prepLayout(t, tmpDir)
		// fill the cache
		repoDir := filepath.Join(tmpDir, url.QueryEscape(testAlpineRepos), testArch)
		err := os.MkdirAll(repoDir, 0o755)
		require.NoError(t, err, "unable to mkdir cache")

		cacheApkFile := filepath.Join(repoDir, testPkgFilename)
		cacheApkDir := strings.TrimSuffix(cacheApkFile, ".apk")

		a.SetClient(&http.Client{
			Transport: &testLocalTransport{root: testPrimaryPkgDir, basenameOnly: true},
		})

		_, err = a.expandPackage(ctx, pkg)
		require.NoErrorf(t, err, "unable to install pkg")
		// check that the package file is in place
		_, err = os.Stat(cacheApkDir)
		require.NoError(t, err, "apk file not found in cache")
		// check that the contents are the same
		exp, err := a.cachedPackage(ctx, pkg, cacheApkDir)
		if err != nil {
			t.Logf("did not find cachedPackage(%q) in %s: %v", pkg.Name, cacheApkDir, err)
			files, err := os.ReadDir(cacheApkDir)
			require.NoError(t, err, "listing "+cacheApkDir)
			for _, f := range files {
				t.Logf("  found %q", f.Name())
			}
		}
		require.NoError(t, err, "unable to read cache apk file")
		f, err := exp.APK()
		require.NoError(t, err, "unable to read cached files as apk")
		defer f.Close()

		apk1, err := io.ReadAll(f)
		require.NoError(t, err, "unable to read cached apk bytes")

		apk2, err := os.ReadFile(filepath.Join(testPrimaryPkgDir, testPkgFilename))
		require.NoError(t, err, "unable to read previous apk file")
		require.Equal(t, apk1, apk2, "apk files do not match")
	})
	t.Run("cache hit no etag", func(t *testing.T) {
		tmpDir := t.TempDir()
		a := prepLayout(t, tmpDir)
		// fill the cache
		repoDir := filepath.Join(tmpDir, url.QueryEscape(testAlpineRepos), testArch)
		err := os.MkdirAll(repoDir, 0o755)
		require.NoError(t, err, "unable to mkdir cache")

		contents, err := os.ReadFile(filepath.Join(testPrimaryPkgDir, testPkgFilename))
		require.NoError(t, err, "unable to read apk file")
		cacheApkFile := filepath.Join(repoDir, testPkgFilename)
		err = os.WriteFile(cacheApkFile, contents, 0o644) //nolint:gosec // we're writing a test file
		require.NoError(t, err, "unable to write cache apk file")

		a.SetClient(&http.Client{
			// use a different root, so we get a different file
			Transport: &testLocalTransport{root: testAlternatePkgDir, basenameOnly: true, headers: map[string][]string{http.CanonicalHeaderKey("etag"): {testEtag}}},
		})
		_, err = a.FetchPackage(ctx, pkg)
		require.NoErrorf(t, err, "unable to install pkg")
		// check that the package file is in place
		_, err = os.Stat(cacheApkFile)
		require.NoError(t, err, "apk file not found in cache")
		// check that the contents are the same as the original
		apk1, err := os.ReadFile(cacheApkFile)
		require.NoError(t, err, "unable to read cache apk file")
		require.Equal(t, apk1, contents, "apk files do not match")
	})
	t.Run("cache hit etag match", func(t *testing.T) {
		tmpDir := t.TempDir()
		a := prepLayout(t, tmpDir)
		// fill the cache
		repoDir := filepath.Join(tmpDir, url.QueryEscape(testAlpineRepos), testArch)
		err := os.MkdirAll(repoDir, 0o755)
		require.NoError(t, err, "unable to mkdir cache")

		contents, err := os.ReadFile(filepath.Join(testPrimaryPkgDir, testPkgFilename))
		require.NoError(t, err, "unable to read apk file")
		cacheApkFile := filepath.Join(repoDir, testPkgFilename)
		err = os.WriteFile(cacheApkFile, contents, 0o644) //nolint:gosec // we're writing a test file
		require.NoError(t, err, "unable to write cache apk file")
		err = os.WriteFile(cacheApkFile+".etag", []byte(testEtag), 0o644) //nolint:gosec // we're writing a test file
		require.NoError(t, err, "unable to write etag")

		a.SetClient(&http.Client{
			// use a different root, so we get a different file
			Transport: &testLocalTransport{root: testAlternatePkgDir, basenameOnly: true, headers: map[string][]string{http.CanonicalHeaderKey("etag"): {testEtag}}},
		})
		_, err = a.FetchPackage(ctx, pkg)
		require.NoErrorf(t, err, "unable to install pkg")
		// check that the package file is in place
		_, err = os.Stat(cacheApkFile)
		require.NoError(t, err, "apk file not found in cache")
		// check that the contents are the same as the original
		apk1, err := os.ReadFile(cacheApkFile)
		require.NoError(t, err, "unable to read cache apk file")
		require.Equal(t, apk1, contents, "apk files do not match")
	})
	t.Run("cache hit etag miss", func(t *testing.T) {
		tmpDir := t.TempDir()
		a := prepLayout(t, tmpDir)
		// fill the cache
		repoDir := filepath.Join(tmpDir, url.QueryEscape(testAlpineRepos), testArch)
		err := os.MkdirAll(repoDir, 0o755)
		require.NoError(t, err, "unable to mkdir cache")

		contents, err := os.ReadFile(filepath.Join(testPrimaryPkgDir, testPkgFilename))
		require.NoError(t, err, "unable to read apk file")
		cacheApkFile := filepath.Join(repoDir, testPkgFilename)
		err = os.WriteFile(cacheApkFile, contents, 0o644) //nolint:gosec // we're writing a test file
		require.NoError(t, err, "unable to write cache apk file")
		err = os.WriteFile(cacheApkFile+".etag", []byte(testEtag), 0o644) //nolint:gosec // we're writing a test file
		require.NoError(t, err, "unable to write etag")

		a.SetClient(&http.Client{
			// use a different root, so we get a different file
			Transport: &testLocalTransport{root: testAlternatePkgDir, basenameOnly: true, headers: map[string][]string{http.CanonicalHeaderKey("etag"): {testEtag + "abcdefg"}}},
		})
		_, err = a.FetchPackage(ctx, pkg)
		require.NoErrorf(t, err, "unable to install pkg")
		// check that the package file is in place
		_, err = os.Stat(cacheApkFile)
		require.NoError(t, err, "apk file not found in cache")
		// check that the contents are the same as the original
		apk1, err := os.ReadFile(cacheApkFile)
		require.NoError(t, err, "unable to read cache apk file")
		apk2, err := os.ReadFile(filepath.Join(testAlternatePkgDir, testPkgFilename))
		require.NoError(t, err, "unable to read testdata apk file")
		require.Equal(t, apk1, apk2, "apk files do not match")
	})
}

func TestAuth_good(t *testing.T) {
	called := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if gotuser, gotpass, ok := r.BasicAuth(); !ok || gotuser != testUser || gotpass != testPass {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		http.FileServer(http.Dir(testPrimaryPkgDir)).ServeHTTP(w, r)
	}))
	defer s.Close()
	host := strings.TrimPrefix(s.URL, "http://")

	repo := Repository{URI: s.URL}
	repoWithIndex := repo.WithIndex(&APKIndex{Packages: []*Package{&testPkg}})
	pkg := NewRepositoryPackage(&testPkg, repoWithIndex)
	ctx := context.Background()

	src := apkfs.NewMemFS()
	err := src.MkdirAll("lib/apk/db", 0o755)
	require.NoError(t, err, "unable to mkdir /lib/apk/db")

	a, err := New(WithFS(src), WithAuthenticator(auth.StaticAuth(host, testUser, testPass)))
	require.NoError(t, err, "unable to create APK")
	err = a.InitDB(ctx)
	require.NoError(t, err)

	_, err = a.FetchPackage(ctx, pkg)
	require.NoErrorf(t, err, "unable to install package")
	require.True(t, called, "did not make request")
}

func TestAuth_bad(t *testing.T) {
	called := false
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if gotuser, gotpass, ok := r.BasicAuth(); !ok || gotuser != testUser || gotpass != testPass {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		http.FileServer(http.Dir(testPrimaryPkgDir)).ServeHTTP(w, r)
	}))
	defer s.Close()
	host := strings.TrimPrefix(s.URL, "http://")

	repo := Repository{URI: s.URL}
	repoWithIndex := repo.WithIndex(&APKIndex{Packages: []*Package{&testPkg}})
	pkg := NewRepositoryPackage(&testPkg, repoWithIndex)
	ctx := context.Background()

	src := apkfs.NewMemFS()
	err := src.MkdirAll("lib/apk/db", 0o755)
	require.NoError(t, err, "unable to mkdir /lib/apk/db")

	a, err := New(WithFS(src), WithAuthenticator(auth.StaticAuth(host, "baduser", "badpass")))
	require.NoError(t, err, "unable to create APK")
	err = a.InitDB(ctx)
	require.NoError(t, err)

	_, err = a.FetchPackage(ctx, pkg)
	require.Error(t, err, "should fail with bad auth")
	require.True(t, called, "did not make request")
}
