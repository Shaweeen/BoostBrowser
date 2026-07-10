package browser

// No default verification/ad/start URLs. New browser instances start on a
// blank page unless the caller explicitly passes startURLs.
var defaultVerificationURLs = []string{}

// BuildLaunchArgs 构建启动参数
func BuildLaunchArgs(args []string, profile *Profile) []string {
	args = append(args, defaultVerificationURLs...)
	return args
}

// GetDefaultVerificationURLs 返回默认验证 URL 列表（用于 CDP 导航）
func GetDefaultVerificationURLs() []string {
	result := make([]string, len(defaultVerificationURLs))
	copy(result, defaultVerificationURLs)
	return result
}
