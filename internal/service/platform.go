package service

import (
	"strings"

	"tfcloud/internal/httpapi/respond"
	"tfcloud/internal/sign"
)

// PlatformService 提供前端「添加账号」所需的平台目录。
// 连接方式只暴露已实现的通道：账号密码（服务端换 Token）与 Cookie/BDUSS 导入。
type PlatformService struct {
	registry *sign.Registry
}

func NewPlatform(registry *sign.Registry) *PlatformService {
	return &PlatformService{registry: registry}
}

var platformConnections = map[string][]string{
	"tieba":     {"bduss"},
	"bilibili":  {"cookie"},
	"netease":   {"cookie"},
	"iqiyi":     {"cookie"},
	"sport":     {"password"},
	"cloud189":  {"password"},
	"picacomic": {"password"},
	"uma":       {"cookie"},
	"yamibo":    {"cookie"},
	"v2ex":      {"cookie"},
}

var platformFieldLabels = map[string]string{
	"tieba:bduss":                 "BDUSS",
	"tieba:stoken":                "STOKEN（可选）",
	"bilibili:sessdata":           "SESSDATA",
	"bilibili:bili_jct":           "bili_jct",
	"bilibili:dede_user_id":       "DedeUserID",
	"bilibili:dede_user_id_ckmd5": "DedeUserID__ckMd5（可选）",
	"netease:music_u":             "MUSIC_U",
	"netease:csrf":                "__csrf",
	"iqiyi:p00001":                "P00001",
	"iqiyi:p00003":                "P00003",
	"sport:username":              "Zepp Life 手机号或邮箱",
	"sport:password":              "Zepp Life 密码",
	"cloud189:username":           "天翼云盘手机号或邮箱",
	"cloud189:password":           "天翼云盘密码",
	"picacomic:username":          "哔咔登录邮箱",
	"picacomic:password":          "哔咔密码",
	"uma:cookie":                  "完整 Cookie（site、joy_jct、DedeUserID、SESSDATA）",
	"yamibo:cookie":               "论坛 Cookie（需已通过 WAF 验证）",
	"v2ex:cookie":                 "完整 Cookie（PB3_SESSION、A2 等）",
}

func (s *PlatformService) List() []map[string]any {
	out := []map[string]any{}
	for _, code := range []string{"tieba", "bilibili", "netease", "iqiyi", "sport", "cloud189", "picacomic", "uma", "yamibo", "v2ex"} {
		plugin, err := s.registry.Get(code)
		if err != nil {
			continue
		}
		out = append(out, s.present(plugin, false))
	}
	return out
}

// ListDetailed 返回带凭据字段的完整目录，供前端一次拉齐。
func (s *PlatformService) ListDetailed() []map[string]any {
	out := []map[string]any{}
	for _, code := range []string{"tieba", "bilibili", "netease", "iqiyi", "sport", "cloud189", "picacomic", "uma", "yamibo", "v2ex"} {
		plugin, err := s.registry.Get(code)
		if err != nil {
			continue
		}
		out = append(out, s.present(plugin, true))
	}
	return out
}

func (s *PlatformService) Get(code string) (map[string]any, error) {
	plugin, err := s.registry.Get(code)
	if err != nil {
		return nil, toAPIError(err)
	}
	if _, ok := platformConnections[code]; !ok {
		return nil, respond.NewError("PLATFORM_NOT_AVAILABLE", "该平台暂未开放接入", 404)
	}
	return s.present(plugin, true), nil
}

func (s *PlatformService) present(plugin sign.Plugin, detail bool) map[string]any {
	meta := plugin.Metadata()
	result := map[string]any{
		"code":               meta.Code,
		"name":               meta.Name,
		"description":        strings.ReplaceAll(meta.Description, "插件", ""),
		"connection_methods": platformConnections[meta.Code],
		"actions":            plugin.SupportedActions(),
	}
	if detail {
		fields := []map[string]any{}
		for _, rule := range plugin.CredentialRules() {
			label := platformFieldLabels[meta.Code+":"+rule.Key]
			if label == "" {
				label = rule.Key
			}
			fields = append(fields, map[string]any{
				"key":      rule.Key,
				"label":    label,
				"required": rule.Required,
			})
		}
		result["credential_fields"] = fields
	}
	return result
}
