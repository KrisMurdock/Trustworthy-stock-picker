package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseWechatCredentialsSupportsLabelAndAssignmentFormats(t *testing.T) {
	for _, test := range []struct {
		name string
		text string
	}{
		{
			name: "label format",
			text: "公众号\n测试账号\nAppID\nwx-file-app\nAppSecret\nfile-secret\nThumbMediaID\ncover-file\nAuthor\nFile Author\n",
		},
		{
			name: "assignment format",
			text: "公众号\n测试账号\napp_id=wx-file-app\napp_secret=file-secret\nthumb_media_id=cover-file\nauthor=File Author\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			credentials, err := parseWechatCredentials(test.text)
			if err != nil {
				t.Fatal(err)
			}
			if credentials.AppID != "wx-file-app" || credentials.AppSecret != "file-secret" ||
				credentials.ThumbMediaID != "cover-file" || credentials.Author != "File Author" {
				t.Fatalf("unexpected credentials: %+v", credentials)
			}
		})
	}
}

func TestParseWechatCredentialsRejectsMissingOrMalformedFields(t *testing.T) {
	for _, text := range []string{
		"公众号\n测试账号\nAppID\nwx-file-app\n",
		"公众号\n测试账号\nAppID\nwx-file-app\nAppSecret\n",
		"公众号\n测试账号\nUnknown\nvalue\nAppID\nwx-file-app\nAppSecret\nfile-secret\n",
		"app_id=wx-file-app\napp_secret=file-secret\nunknown=value\n",
	} {
		if _, err := parseWechatCredentials(text); err == nil || strings.Contains(err.Error(), "file-secret") {
			t.Fatalf("accepted malformed credentials or leaked a value: %v", err)
		}
	}
}

func TestWechatDraftLoadsCredentialsFileAndEnvironmentOverrides(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.yaml")
	text := valid + `first_stage:
  enabled: true
  push_enabled: true
  budget: 10
second_stage:
  enabled: true
  push_enabled: true
wechat:
  enabled: true
  credentials_file: wechat.config
`
	if err := os.WriteFile(configPath, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	credentials := "公众号\n测试账号\nAppID\nwx-file-app\nAppSecret\nfile-secret\nThumbMediaID\ncover-file\nAuthor\nFile Author\n"
	if err := os.WriteFile(filepath.Join(root, "wechat.config"), []byte(credentials), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WECHAT_APP_ID", "")
	t.Setenv("WECHAT_APP_SECRET", "")
	t.Setenv("WECHAT_THUMB_MEDIA_ID", "")
	t.Setenv("STOCK_WATCHER_WECHAT_AUTHOR", "")

	c, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if c.Wechat.AppID != "wx-file-app" || c.Wechat.AppSecret != "file-secret" ||
		c.Wechat.ThumbMediaID != "cover-file" || c.Wechat.Author != "File Author" {
		t.Fatalf("credentials file was not loaded: %+v", c.Wechat)
	}

	t.Setenv("WECHAT_APP_ID", "wx-env-app")
	t.Setenv("WECHAT_APP_SECRET", "env-secret")
	t.Setenv("WECHAT_THUMB_MEDIA_ID", "cover-env")
	t.Setenv("STOCK_WATCHER_WECHAT_AUTHOR", "Env Author")
	c, err = Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if c.Wechat.AppID != "wx-env-app" || c.Wechat.AppSecret != "env-secret" ||
		c.Wechat.ThumbMediaID != "cover-env" || c.Wechat.Author != "Env Author" {
		t.Fatalf("environment did not override credentials file: %+v", c.Wechat)
	}
}
