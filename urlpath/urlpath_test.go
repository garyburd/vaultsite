package urlpath

import "testing"

func TestEncode(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"/", "/"},
		{"/articles/Hello World/", "/articles/Hello%20World/"},
		{"/files/Route Map.pdf", "/files/Route%20Map.pdf"},
		{"/café/", "/caf%C3%A9/"},
		{"/A-Z.a_z~0/", "/A-Z.a_z~0/"},
		{"/a+b&c=d$e@f/", "/a%2Bb%26c%3Dd%24e%40f/"},
		{"/100%/", "/100%25/"},
		{"/a\x00b", "/a%00b"},
	}
	for _, tt := range tests {
		if got := Encode(tt.in); got != tt.want {
			t.Errorf("Encode(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCanonical(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{"/%41bc/", "/Abc/", false},
		{"/caf%c3%a9/", "/caf%C3%A9/", false},
		{"/caf%C3%A9/", "/caf%C3%A9/", false},
		{"/articles/Hello World/", "/articles/Hello%20World/", false},
		{"/articles/Hello%20World/", "/articles/Hello%20World/", false},
		// Unlike a query, a path has no "+" for a space.
		{"/a+b/", "/a%2Bb/", false},
		{"/x%", "", true},
		{"/x%2", "", true},
		{"/x%zz", "", true},
	}
	for _, tt := range tests {
		got, err := Canonical(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("Canonical(%q) = %q, %v; want %q, error %v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestKey(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{"/", "index.html", false},
		{"/articles/Hello%20World/", "articles/Hello World/index.html", false},
		{"/articles/", "articles/index.html", false},
		{"/files/Route%20Map.pdf", "files/Route Map.pdf", false},
		{"/foo/index.html", "foo/index.html", false},
		{"/foo/", "foo/index.html", false},
		{"/Foo/", "Foo/index.html", false},
		{"/about", "about", false},
		{"/.well-known/x", ".well-known/x", false},
		{"/a..b/", "a..b/index.html", false},

		{"", "", true},
		{"relative/", "", true},
		{"//", "", true},
		{"/a//b", "", true},
		{"/a/./b", "", true},
		{"/a/../b", "", true},
		{"/a/%2E%2E/b", "", true},
		{"/..", "", true},
		{"/a%2Fb", "", true},
		{"/a%2fb/", "", true},
		{"/a%00b", "", true},
		{"/a%zz", "", true},
	}
	for _, tt := range tests {
		got, err := Key(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("Key(%q) = %q, %v; want %q, error %v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestJoin(t *testing.T) {
	tests := []struct {
		parts []string
		want  string
	}{
		{[]string{"/tags", "photos/alaska", "/"}, "/tags/photos/alaska/"},
		{[]string{"/a b", "c"}, "/a%20b/c"},
		{[]string{"a", "b"}, "a/b"},
		{[]string{"/a/", "/b/"}, "/a/b/"},
		{[]string{"/a//b", ""}, "/a/b"},
		{[]string{"/tags", "café", "/"}, "/tags/caf%C3%A9/"},
		{[]string{"/"}, "/"},
		{[]string{"/", "/"}, "/"},
		{[]string{"", "/"}, "/"},
		{[]string{""}, ""},
		{nil, ""},
	}
	for _, tt := range tests {
		if got := Join(tt.parts...); got != tt.want {
			t.Errorf("Join(%q) = %q, want %q", tt.parts, got, tt.want)
		}
	}
}

func TestCheckPermalink(t *testing.T) {
	good := []string{
		"/",
		"/about",
		"/about/",
		"/first-walk/",
		"/%41bc/",
		"/caf%c3%a9/",
		"/a.b_c~d/e.html",
		"/foo/index.html",
	}
	for _, s := range good {
		if err := CheckPermalink(s); err != nil {
			t.Errorf("CheckPermalink(%q) = %v, want nil", s, err)
		}
	}
	bad := []string{
		"",
		"about/",
		"//",
		"/a//b/",
		"/a b/",
		"/café/",
		"/a?b",
		"/a#b",
		"/a%2Fb/",
		"/a%00b/",
		"/a/./b/",
		"/a/../b/",
		"/a%zz/",
		"/a%2",
	}
	for _, s := range bad {
		if err := CheckPermalink(s); err == nil {
			t.Errorf("CheckPermalink(%q) = nil, want an error", s)
		}
	}
}
