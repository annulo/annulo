package creght

import (
	"context"
	"net/http"
	"strings"

	"github.com/annulo/annulo/internal/i18n"
)

// creght 有几个分开运营的集群：同一套接口，账号、项目、模板各是各的，token 也按集群分开存。
// 用户在设置里选用哪个（config.Creght），登录、项目列表、新建项目都在那个集群。

// DefaultHost 是没选过集群时用的。
const DefaultHost = "https://creght.cn"

type Cluster struct {
	Host string
	name [2]string // 中文、英文
	// Hidden：不在区域列表里给人选（2026-10-06 起 talizen.com 不再列出）；已经选了它的照常用，正在用时列表里仍显示
	Hidden bool
}

func (c Cluster) Name() string { return i18n.T(c.name[0], c.name[1]) }

var Clusters = []Cluster{
	{Host: "https://creght.cn", name: [2]string{"creght.cn（中国）", "creght.cn (China)"}},
	{Host: "https://creght.com", name: [2]string{"creght.com（国际）", "creght.com (International)"}},
	{Host: "https://talizen.com", name: [2]string{"talizen.com", "talizen.com"}, Hidden: true},
}

// ClusterOf 按地址找集群（地址大小写、结尾斜杠不影响）；不是已知集群返回 nil。
func ClusterOf(host string) *Cluster {
	h := normHost(host)
	for i := range Clusters {
		if Clusters[i].Host == h {
			return &Clusters[i]
		}
	}
	return nil
}

// NormHost 把 API 地址规整成 scheme://host，用来比较两个地址是不是同一个集群。
func NormHost(h string) string { return normHost(h) }

// OpsTemplate 是集群里登记的 Shuttle 模板（开了公开复制的运营后台项目，不在官网模板中心）。
type OpsTemplate struct {
	ProjectID   string            `json:"project_id"`
	SiteID      string            `json:"site_id"`
	NameLocales map[string]string `json:"name_locales"`
	DescLocales map[string]string `json:"desc_locales"`
	Sort        int               `json:"sort"`
	VersionNo   int               `json:"version_no"`
	PreviewURL  string            `json:"preview_url"`
	// Paid：付费模板；Entitled：当前账号能用（免费模板永远 true，没连 creght 时付费模板是 false）；Packages：开通哪个套餐能解锁
	Paid     bool         `json:"paid"`
	Entitled bool         `json:"entitled"`
	Packages []TplPackage `json:"packages"`
}

// TplPackage 是能解锁付费模板的套餐，价格单位是分（美分）。
type TplPackage struct {
	ID            int               `json:"id"`
	NameLocales   map[string]string `json:"name_locales"`
	PriceByMonth  int               `json:"price_by_month"`
	PriceByYear   int               `json:"price_by_year"`
	PriceCurrency string            `json:"price_currency"`
}

// OpsTemplates 列出这个集群的 Shuttle 模板，按推荐顺序（第一个是默认）。公开接口；连着 creght 时带上 token，entitled 按这个账号算。
func (c *Client) OpsTemplates(ctx context.Context) ([]OpsTemplate, error) {
	var ret struct {
		List []OpsTemplate `json:"list"`
	}
	tok, _ := ReadToken(c.APIHost)
	if err := c.send(ctx, http.MethodGet, "/api/p/ops_tpl_list", nil, nil, &ret, tok); err != nil {
		return nil, err
	}
	return ret.List, nil
}

// SubscribeURL 是在浏览器里开通套餐的页面（付费模板没权限时的「去开通」）。2026-10-05：只有 talizen.com 有自助开通页 /subscribe（不能指定套餐、开通后不跳回）；
// creght.cn / creght.com 还没有，返回空，界面就不放「去开通」。开通后重新拉一次模板列表看 entitled。
func SubscribeURL(apiHost string, packageID int) string {
	if strings.HasSuffix(normHost(apiHost), "://talizen.com") {
		return normHost(apiHost) + "/subscribe"
	}
	return ""
}
