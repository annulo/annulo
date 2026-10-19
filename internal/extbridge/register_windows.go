package extbridge

import "golang.org/x/sys/windows/registry"

// setRegistry 在 HKCU\<key> 写默认值（Chrome 从这里找 native host 的清单）
func setRegistry(key, value string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, key, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue("", value)
}
