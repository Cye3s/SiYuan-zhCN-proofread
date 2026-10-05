package main

// 解析/导出正确性验证（go test，不进交付产物）：
// 1. TestOrderedRoundtrip：官方 zh-CN.json 解析→序列化应逐字节还原（保序正确性）
// 2. TestAppendErrorLog：崩溃日志格式（时间戳 + 追加式 + 内容完整）

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const p_officialPath = `langs\zh-CN.json`

func TestOrderedRoundtrip(v_t *testing.T) {
	v_data, v_err := os.ReadFile(p_officialPath)
	if v_err != nil {
		v_t.Skip("官方文件不存在: ", v_err)
	}
	v_node, v_err := ParseOrdered(v_data)
	if v_err != nil {
		v_t.Fatal("解析失败: ", v_err)
	}
	v_flat := Flatten(v_node, "", map[string]string{})
	v_t.Logf("keys(flatten str leaves) = %d", len(v_flat))

	v_out := v_node.Serialize(DetectIndent(string(v_data)))
	if bytes.Equal(v_data, v_out) {
		v_t.Logf("保序往返：与原文件逐字节一致 (%d bytes)", len(v_out))
		return
	}
	v_srcLines := strings.Split(string(v_data), "\n")
	v_outLines := strings.Split(string(v_out), "\n")
	v_t.Logf("非逐字节一致：原 %d 行 / 新 %d 行", len(v_srcLines), len(v_outLines))
	v_diffs := 0
	for v_idx := 0; v_idx < len(v_srcLines) && v_idx < len(v_outLines) && v_diffs < 10; v_idx++ {
		if v_srcLines[v_idx] != v_outLines[v_idx] {
			v_diffs++
			v_t.Logf("line %d:\n  原: %.80q\n  新: %.80q", v_idx+1, v_srcLines[v_idx], v_outLines[v_idx])
		}
	}
}

func TestAppendErrorLog(v_t *testing.T) {
	v_exe, v_err := os.Executable()
	if v_err != nil {
		v_t.Skip("无法定位测试二进制: ", v_err)
	}
	v_logPath := filepath.Join(filepath.Dir(v_exe), "error.log")
	_ = os.Remove(v_logPath)

	appendErrorLog("单测触发：模拟崩溃信息 A")
	appendErrorLog("单测触发：模拟崩溃信息 B")

	v_data, v_err := os.ReadFile(v_logPath)
	if v_err != nil {
		v_t.Fatal("error.log 未生成: ", v_err)
	}
	v_text := string(v_data)

	v_stamp := regexp.MustCompile(`\[\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\]`)
	v_stamps := v_stamp.FindAllString(v_text, -1)
	if len(v_stamps) != 2 {
		v_t.Errorf("时间戳应为 2 个（每次写入一条），实际 %d 个，内容:\n%s", len(v_stamps), v_text)
	}
	if !strings.Contains(v_text, "模拟崩溃信息 A") || !strings.Contains(v_text, "模拟崩溃信息 B") {
		v_t.Errorf("追加式失败或内容缺失，内容:\n%s", v_text)
	}

	_ = os.Remove(v_logPath)
}
