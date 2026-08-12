// Package speedtest 提供TG频道和搜索插件的测速能力。
//
// 测速方式（对频道和插件一致）：
//  1. 连通性测试：先发起一次轻量请求，判断目标是否可达；不可达则直接判定失败，不再进行第二步。
//  2. 实际延迟测试：连通性测试通过后，再发起一次"真实"请求（频道走真实搜索URL，
//     插件走真实Search方法+固定测试关键词），记录本次请求耗时作为延迟。
//
// 两步测速全程串行执行（不并发），避免测速阶段本身对网络/CPU造成压力从而影响结果准确性。
//
// 测速结果会保存到本地JSON文件中；下次启动时如果结果文件已存在，会直接加载复用，
// 只对文件中不存在的"新增"频道/插件进行补测，补测同样是串行的，测完追加保存。
package speedtest

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"sort"
	"time"

	"pansou/model"
	"pansou/plugin"
	"pansou/util"
)

// TestKeyword 用于插件实际延迟测速的固定测试关键词
// 选择一个大概率有结果、但不会太重的通用词
const TestKeyword = "奔跑吧"

// SourceType 测速目标类型
type SourceType string

const (
	SourceTypeChannel SourceType = "channel" // TG频道
	SourceTypePlugin  SourceType = "plugin"  // 搜索插件
)

// Result 单个测速目标（频道或插件）的测速结果
type Result struct {
	Name       string     `json:"name"`                 // 频道名或插件名
	Type       SourceType `json:"type"`                 // 类型：channel/plugin
	Reachable  bool       `json:"reachable"`             // 连通性测试是否通过
	LatencyMs  int64      `json:"latency_ms"`             // 实际请求延迟（毫秒），仅Reachable为true时有效
	Success    bool       `json:"success"`               // 是否完整完成两步测速且成功拿到结果
	Error      string     `json:"error,omitempty"`       // 失败原因（如果有）
	TestedAt   time.Time  `json:"tested_at"`             // 本次测速时间
}

// Report 测速结果报告，对应保存到磁盘的JSON文件结构
type Report struct {
	Version   int               `json:"version"`    // 结果文件格式版本，便于未来升级兼容
	UpdatedAt time.Time         `json:"updated_at"` // 最近一次更新时间
	Channels  map[string]Result `json:"channels"`   // 频道名 -> 测速结果
	Plugins   map[string]Result `json:"plugins"`    // 插件名 -> 测速结果
}

const reportVersion = 1

// newEmptyReport 创建一个空的测速报告
func newEmptyReport() *Report {
	return &Report{
		Version:  reportVersion,
		Channels: make(map[string]Result),
		Plugins:  make(map[string]Result),
	}
}

// LoadReport 从磁盘加载已有的测速结果；文件不存在或解析失败时返回一个空报告（不视为错误）
func LoadReport(path string) *Report {
	report := newEmptyReport()

	data, err := ioutil.ReadFile(path)
	if err != nil {
		// 文件不存在或不可读，视为首次运行，返回空报告
		return report
	}

	var loaded Report
	if err := json.Unmarshal(data, &loaded); err != nil {
		// 文件损坏，视为首次运行，返回空报告（避免因为一个坏文件导致程序无法启动）
		fmt.Printf("[测速] 结果文件解析失败，将重新测速: %v\n", err)
		return report
	}

	if loaded.Channels == nil {
		loaded.Channels = make(map[string]Result)
	}
	if loaded.Plugins == nil {
		loaded.Plugins = make(map[string]Result)
	}

	return &loaded
}

// SaveReport 将测速结果保存到磁盘（原子写入：先写临时文件再重命名，避免写到一半被读到）
func SaveReport(path string, report *Report) error {
	report.UpdatedAt = time.Now()

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化测速结果失败: %v", err)
	}

	tmpPath := path + ".tmp"
	if err := ioutil.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("写入测速结果临时文件失败: %v", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("保存测速结果文件失败: %v", err)
	}

	return nil
}

// testChannelConnectivity 测试频道连通性：只判断能否连上、能否拿到响应，不关心内容
func testChannelConnectivity(channel string, timeout time.Duration) error {
	url := util.BuildSearchURL(channel, "", "")

	client := util.GetHTTPClient()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}

	// 独立超时控制，避免使用全局client的默认超时（可能过长或过短）
	ctxClient := &http.Client{
		Transport: client.Transport,
		Timeout:   timeout,
	}

	resp, err := ctxClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return fmt.Errorf("服务端错误，状态码: %d", resp.StatusCode)
	}

	return nil
}

// testChannelLatency 测试频道实际延迟：发起一次真实搜索请求（使用测试关键词），记录耗时
func testChannelLatency(channel string, timeout time.Duration) (time.Duration, error) {
	url := util.BuildSearchURL(channel, TestKeyword, "")

	client := util.GetHTTPClient()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, err
	}

	ctxClient := &http.Client{
		Transport: client.Transport,
		Timeout:   timeout,
	}

	start := time.Now()
	resp, err := ctxClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	_, err = ioutil.ReadAll(resp.Body)
	elapsed := time.Since(start)
	if err != nil {
		return 0, err
	}

	if resp.StatusCode >= 500 {
		return 0, fmt.Errorf("服务端错误，状态码: %d", resp.StatusCode)
	}

	return elapsed, nil
}

// TestChannel 对单个频道执行完整的两步测速（连通性 -> 实际延迟）
func TestChannel(channel string, timeout time.Duration) Result {
	result := Result{
		Name:     channel,
		Type:     SourceTypeChannel,
		TestedAt: time.Now(),
	}

	// 第一步：连通性测试
	if err := testChannelConnectivity(channel, timeout); err != nil {
		result.Reachable = false
		result.Success = false
		result.Error = "连通性测试失败: " + err.Error()
		return result
	}
	result.Reachable = true

	// 第二步：实际延迟测试
	elapsed, err := testChannelLatency(channel, timeout)
	if err != nil {
		result.Success = false
		result.Error = "延迟测试失败: " + err.Error()
		return result
	}

	result.Success = true
	result.LatencyMs = elapsed.Milliseconds()
	return result
}

// testPluginConnectivity 测试插件连通性：用一次轻量Search调用判断插件底层服务是否可达
// 注：多数插件没有独立的"仅测连通性"接口，这里复用Search本身，但会在超时上做严格限制，
// 避免连通性测试阶段就消耗过长时间。
func testPluginConnectivity(p plugin.AsyncSearchPlugin, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("插件panic: %v", r)
			}
		}()
		_, err := p.Search(TestKeyword, map[string]interface{}{})
		done <- err
	}()

	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return fmt.Errorf("连通性测试超时(%v)", timeout)
	}
}

// testPluginLatency 测试插件实际延迟：执行一次真实Search调用并计时
func testPluginLatency(p plugin.AsyncSearchPlugin, timeout time.Duration) (time.Duration, []model.SearchResult, error) {
	type searchOutcome struct {
		results []model.SearchResult
		err     error
	}

	done := make(chan searchOutcome, 1)
	start := time.Now()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- searchOutcome{nil, fmt.Errorf("插件panic: %v", r)}
			}
		}()
		results, err := p.Search(TestKeyword, map[string]interface{}{})
		done <- searchOutcome{results, err}
	}()

	select {
	case outcome := <-done:
		elapsed := time.Since(start)
		if outcome.err != nil {
			return 0, nil, outcome.err
		}
		return elapsed, outcome.results, nil
	case <-time.After(timeout):
		return 0, nil, fmt.Errorf("延迟测试超时(%v)", timeout)
	}
}

// TestPlugin 对单个插件执行完整的两步测速（连通性 -> 实际延迟）
// 说明：由于多数插件的Search方法本身就是一次完整请求，这里的"连通性测试"和"延迟测试"
// 会各自独立发起一次真实Search调用（共两次），以符合"先测连通性、再测延迟"的两步流程；
// 两次调用都会计入总的测速耗时，属于一次性开销。
func TestPlugin(p plugin.AsyncSearchPlugin, timeout time.Duration) Result {
	result := Result{
		Name:     p.Name(),
		Type:     SourceTypePlugin,
		TestedAt: time.Now(),
	}

	// 第一步：连通性测试
	if err := testPluginConnectivity(p, timeout); err != nil {
		result.Reachable = false
		result.Success = false
		result.Error = "连通性测试失败: " + err.Error()
		return result
	}
	result.Reachable = true

	// 第二步：实际延迟测试
	elapsed, _, err := testPluginLatency(p, timeout)
	if err != nil {
		result.Success = false
		result.Error = "延迟测试失败: " + err.Error()
		return result
	}

	result.Success = true
	result.LatencyMs = elapsed.Milliseconds()
	return result
}

// RunAndPersist 对给定的频道列表和插件列表执行测速，并与磁盘上已有结果合并保存。
// 已存在于结果文件中的频道/插件不会重复测速；只有新增的才会被测速。
// 整个过程串行执行（频道、插件均串行；频道和插件之间也顺序执行），不并发。
//
// 返回合并后的最新报告。
func RunAndPersist(resultPath string, channels []string, plugins []plugin.AsyncSearchPlugin, timeout time.Duration) *Report {
	report := LoadReport(resultPath)

	// 记录本次是否有新增测速，决定是否需要重新保存
	hasNewData := false

	// 频道测速（串行，只测未测过的）
	for _, channel := range channels {
		if _, exists := report.Channels[channel]; exists {
			continue
		}
		fmt.Printf("[测速] 频道 %s 测速中...\n", channel)
		result := TestChannel(channel, timeout)
		report.Channels[channel] = result
		hasNewData = true

		if result.Success {
			fmt.Printf("[测速] 频道 %s 完成，延迟: %dms\n", channel, result.LatencyMs)
		} else {
			fmt.Printf("[测速] 频道 %s 失败: %s\n", channel, result.Error)
		}
	}

	// 插件测速（串行，只测未测过的）
	for _, p := range plugins {
		name := p.Name()
		if _, exists := report.Plugins[name]; exists {
			continue
		}
		fmt.Printf("[测速] 插件 %s 测速中...\n", name)
		result := TestPlugin(p, timeout)
		report.Plugins[name] = result
		hasNewData = true

		if result.Success {
			fmt.Printf("[测速] 插件 %s 完成，延迟: %dms\n", name, result.LatencyMs)
		} else {
			fmt.Printf("[测速] 插件 %s 失败: %s\n", name, result.Error)
		}
	}

	if hasNewData {
		if err := SaveReport(resultPath, report); err != nil {
			fmt.Printf("[测速] 保存结果文件失败: %v\n", err)
		} else {
			fmt.Printf("[测速] 结果已保存至: %s\n", resultPath)
		}
	}

	return report
}

// selectTopN 是SelectTopChannels/SelectTopPlugins共用的核心筛选逻辑。
// allNames: 全部候选名称（顺序即传入顺序，用于"未测速/失败时按原顺序补齐"）
// results: 名称 -> 测速结果 的映射（可能不包含全部allNames，缺失的视为未测速）
// topN: 需要选出的数量；<=0表示不限制，直接返回全部allNames
//
// 选择规则：
//  1. 成功的结果按延迟从低到高排序，优先选入
//  2. 如果数量不够topN，用失败/未测速的（按allNames中原始顺序）补齐
//  3. 最终数量不超过len(allNames)
func selectTopN(allNames []string, results map[string]Result, topN int) []string {
	if topN <= 0 || topN >= len(allNames) {
		return allNames
	}

	var successNames []string
	var otherNames []string

	for _, name := range allNames {
		result, exists := results[name]
		if exists && result.Success {
			successNames = append(successNames, name)
		} else {
			otherNames = append(otherNames, name)
		}
	}

	// 成功的按延迟升序排序
	sort.Slice(successNames, func(i, j int) bool {
		return results[successNames[i]].LatencyMs < results[successNames[j]].LatencyMs
	})

	selected := make([]string, 0, topN)
	selected = append(selected, successNames...)

	// 不够则用失败/未测速的补齐（保持原始顺序，不做额外排序）
	if len(selected) < topN {
		need := topN - len(selected)
		if need > len(otherNames) {
			need = len(otherNames)
		}
		selected = append(selected, otherNames[:need]...)
	}

	if len(selected) > topN {
		selected = selected[:topN]
	}

	return selected
}

// SelectTopChannels 根据测速报告，从candidateChannels中选出延迟最低的topN个频道名。
// topN<=0时返回全部候选（不筛选）。
func SelectTopChannels(report *Report, candidateChannels []string, topN int) []string {
	return selectTopN(candidateChannels, report.Channels, topN)
}

// SelectTopPluginNames 根据测速报告，从candidatePlugins中选出延迟最低的topN个插件名。
// topN<=0时返回全部候选（不筛选）。
func SelectTopPluginNames(report *Report, candidatePlugins []plugin.AsyncSearchPlugin, topN int) []string {
	names := make([]string, 0, len(candidatePlugins))
	for _, p := range candidatePlugins {
		names = append(names, p.Name())
	}
	return selectTopN(names, report.Plugins, topN)
}
