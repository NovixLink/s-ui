package sub

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/alireza0/s-ui/database"
	"github.com/alireza0/s-ui/database/model"
	"gopkg.in/yaml.v3"
)

func TestParseCustomHeaders(t *testing.T) {
	testCases := []struct {
		name string
		in   string
		want []customHeader
	}{
		{"empty string", "", nil},
		{"whitespace only", "   \n\t  \r\n   ", nil},
		{"comment", "# 424242", nil},
		{
			"extra spaces",
			"\n\tannounce :  42! \n\tannounce-url: t.me/4242\n\t",
			[]customHeader{{"announce", "42!"}, {"announce-url", "t.me/4242"}},
		},
		{
			"values keep their colons",
			"Profile-Web-Page-Url: https://sub.example.com:8443/login?redirect=https://a.com",
			[]customHeader{{"Profile-Web-Page-Url", "https://sub.example.com:8443/login?redirect=https://a.com"}},
		},
		{
			"crlf line endings and comments",
			"# header 1\r\nX-Custom: value1\r\n// comment 2\r\nX-Other: value2\r\n",
			[]customHeader{{"X-Custom", "value1"}, {"X-Other", "value2"}},
		},
		{
			"malformed lines are skipped",
			"random text without colon\n: missing key\nempty value: \n:\nvalid-key: ok\n",
			[]customHeader{{"valid-key", "ok"}},
		},
		{
			// A name an HTTP header cannot carry would go out malformed.
			"invalid names are skipped",
			"Bad Name: x\nBad(Name): x\nGood-Name: x\n",
			[]customHeader{{"Good-Name", "x"}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseCustomHeaders(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// Existing subscription names may contain slashes. Both their already issued
// raw URLs and URLs escaped once must reach the same enabled client in every
// format, without broadening the configured subscription prefix or decoding twice.
func TestSubscriptionNamesWithSlashesHTTP(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "subscription-paths.db")); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	const uri = "vless://bf000d23-0752-40b4-affe-68f7707a9661@192.0.2.1:443?security=reality&sni=relay.test&pbk=test-key&sid=0000000000000001#Line"
	links, err := json.Marshal([]Link{{Type: "external", Uri: uri}})
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"plain-client", "alpha/beta/gamma", "alpha//beta/gamma/delta", "/leading/name", "trailing/name/", "plus+slash/name"}
	for _, name := range append(append([]string(nil), names...), "disabled/path/name") {
		client := model.Client{Enable: true, Name: name, Remark: "Route fixture", Config: json.RawMessage(`{}`), Inbounds: json.RawMessage(`[]`), Links: links}
		if err := db.Create(&client).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&model.Client{}).Where("name = ?", "disabled/path/name").Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	server := NewServer()
	if err := server.SetSubPath("/subscriptions/fixture/"); err != nil {
		t.Fatal(err)
	}
	router, err := server.initRouter()
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(router)
	t.Cleanup(httpServer.Close)
	client := httpServer.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	request := func(t *testing.T, method, path string, want int) []byte {
		t.Helper()
		req, err := http.NewRequest(method, httpServer.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("%s %s: HTTP %d, want %d: %s", method, path, response.StatusCode, want, body)
		}
		if want == http.StatusOK {
			for _, header := range []string{"Subscription-Userinfo", "Profile-Update-Interval", "Profile-Title", "Content-Disposition"} {
				if response.Header.Get(header) == "" {
					t.Errorf("%s %s: missing %s", method, path, header)
				}
			}
		}
		if method == http.MethodHead && len(body) != 0 {
			t.Fatal("HEAD returned a response body")
		}
		return body
	}
	encoded, err := (&SubService{}).GetSubEncode()
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"", "?format=json", "?format=clash"} {
		t.Run("format="+query, func(t *testing.T) {
			baseline := request(t, http.MethodGet, "/subscriptions/fixture/"+names[0]+query, http.StatusOK)
			switch query {
			case "":
				plain := baseline
				if encoded {
					plain, err = base64.StdEncoding.DecodeString(string(baseline))
					if err != nil {
						t.Fatal(err)
					}
				}
				if string(plain) != uri {
					t.Fatalf("URI changed: %s", plain)
				}
			case "?format=json":
				var result struct {
					Outbounds []struct {
						Type       string
						ServerPort int `json:"server_port"`
					}
				}
				if err := json.Unmarshal(baseline, &result); err != nil {
					t.Fatal(err)
				}
				found := false
				for _, outbound := range result.Outbounds {
					if outbound.Type == "vless" && outbound.ServerPort == 443 {
						found = true
					}
				}
				if !found {
					t.Fatalf("missing VLESS 443 outbound: %s", baseline)
				}
			case "?format=clash":
				var result struct {
					Proxies []struct {
						Type string
						Port int
					}
				}
				if err := yaml.Unmarshal(baseline, &result); err != nil {
					t.Fatal(err)
				}
				if len(result.Proxies) != 1 || result.Proxies[0].Type != "vless" || result.Proxies[0].Port != 443 {
					t.Fatalf("missing VLESS 443 proxy: %s", baseline)
				}
			}
			for _, name := range names {
				for _, path := range []string{name, url.PathEscape(name)} {
					path = "/subscriptions/fixture/" + path + query
					if body := request(t, http.MethodGet, path, http.StatusOK); string(body) != string(baseline) {
						t.Fatalf("%s changed the subscription content", path)
					}
					request(t, http.MethodHead, path, http.StatusOK)
				}
			}
			for _, name := range []string{"missing/path/name", "disabled/path/name"} {
				for _, path := range []string{name, url.PathEscape(name)} {
					path = "/subscriptions/fixture/" + path + query
					request(t, http.MethodGet, path, http.StatusBadRequest)
					request(t, http.MethodHead, path, http.StatusBadRequest)
				}
			}
			doubleEncoded := "/subscriptions/fixture/" + url.PathEscape(url.PathEscape(names[1])) + query
			request(t, http.MethodGet, doubleEncoded, http.StatusBadRequest)
			request(t, http.MethodHead, doubleEncoded, http.StatusBadRequest)
			for _, path := range []string{"/sub/", "/subscriptions/fixtures/", "/other/subscriptions/fixture/"} {
				request(t, http.MethodGet, path+names[0]+query, http.StatusNotFound)
				request(t, http.MethodHead, path+names[0]+query, http.StatusNotFound)
			}
		})
	}
}
