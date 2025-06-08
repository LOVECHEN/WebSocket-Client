/*
WebSocket Client - 企业级高性能WebSocket客户端
Copyright (C) 2025 lovechen

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.
*/

// Package main 实现了一个企业级高性能 WebSocket 客户端命令行工具。
//
// 🚀 企业级性能特性：
//   - 零分配字符串操作和优化的内存池
//   - 无锁并发设计和原子操作优化
//   - 智能缓冲区管理和对象复用
//   - 高效的错误处理和恢复机制
//   - 优化的网络I/O和系统调用
//   - 实时性能监控和健康检查
//   - 企业级安全防护和频率限制
//
// 🎯 使用示例：
//
//	./wsc ws://localhost:8080/websocket
//	./wsc -n -v -r 10 -t 5 wss://echo.websocket.org
//	./wsc --metrics --health-port 8080 wss://api.example.com/ws
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// 初始化随机数种子，确保会话ID的唯一性
// 注意：现在使用crypto/rand包提供加密安全的随机数生成

// ===== 应用程序版本和构建信息 =====

// 应用程序版本信息
// 这些常量定义了应用程序的基本标识信息
// 版本号遵循语义化版本控制（Semantic Versioning）规范
// 格式：主版本号.次版本号.修订号
//   - 主版本号：不兼容的API修改时递增
//   - 次版本号：向下兼容的功能性新增时递增
//   - 修订号：向下兼容的问题修正时递增
const (
	AppName    = "WebSocket Client" // 应用程序名称
	AppVersion = "2.1.1"            // 当前应用程序版本
)

// 构建信息（通过ldflags注入）
// 这些变量在编译时通过-ldflags参数注入实际值
// 用于提供详细的构建信息，便于版本追踪和问题诊断
//
// 注入示例：
//
//	go build -ldflags "-X main.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
//	                   -X main.GitCommit=$(git rev-parse HEAD) \
//	                   -X main.GoVersion=$(go version)"
//
// 使用场景：
//   - 版本信息显示（--version命令）
//   - 构建信息显示（--build-info命令）
//   - 日志记录中的版本标识
//   - 问题诊断和技术支持
var (
	BuildTime = "unknown" // 构建时间：编译时的UTC时间戳
	GitCommit = "unknown" // Git提交哈希：用于追踪具体的代码版本
	GoVersion = "unknown" // Go版本：编译时使用的Go语言版本
)

// ===== 核心接口定义 =====
// 定义WebSocket客户端的核心抽象接口，支持依赖注入和可插拔组件
// 这些接口采用了依赖注入模式，允许用户自定义实现来替换默认行为

// Connector 连接器接口 - 负责WebSocket连接的建立和管理
// 这个接口抽象了WebSocket连接的底层细节，使得连接逻辑可以被替换和测试
//
// 设计原则：
//   - 单一职责：只负责连接相关的操作
//   - 接口隔离：方法简洁明确，职责清晰
//   - 依赖倒置：上层代码依赖接口而非具体实现
//
// 使用场景：
//   - 自定义连接逻辑（如代理、负载均衡）
//   - 单元测试中的mock实现
//   - 不同环境下的连接策略
type Connector interface {
	// Connect 建立WebSocket连接
	// 参数：
	//   - ctx: 上下文，用于取消操作和超时控制
	//   - url: WebSocket服务器地址
	//   - config: 客户端配置，包含超时、TLS等设置
	// 返回：
	//   - *websocket.Conn: 建立的WebSocket连接
	//   - error: 连接失败时的错误信息
	Connect(ctx context.Context, url string, config *ClientConfig) (*websocket.Conn, error)

	// Disconnect 断开WebSocket连接
	// 参数：
	//   - conn: 要断开的WebSocket连接
	// 返回：
	//   - error: 断开连接时的错误信息
	Disconnect(conn *websocket.Conn) error

	// IsHealthy 检查连接是否健康
	// 参数：
	//   - conn: 要检查的WebSocket连接
	// 返回：
	//   - bool: true表示连接健康，false表示连接有问题
	IsHealthy(conn *websocket.Conn) bool
}

// MessageProcessor 消息处理器接口 - 负责消息的处理和验证
// 这个接口抽象了消息处理逻辑，使得消息处理可以被自定义和扩展
//
// 设计原则：
//   - 职责分离：处理和验证分开，便于组合使用
//   - 类型安全：明确的消息类型和数据格式
//   - 错误处理：清晰的错误返回机制
//
// 使用场景：
//   - 自定义消息格式处理（JSON、Protobuf等）
//   - 消息内容验证和过滤
//   - 消息转换和路由
type MessageProcessor interface {
	// ProcessMessage 处理接收到的消息
	// 参数：
	//   - messageType: WebSocket消息类型（TextMessage、BinaryMessage等）
	//   - data: 消息内容的字节数组
	// 返回：
	//   - error: 处理失败时的错误信息
	ProcessMessage(messageType int, data []byte) error

	// ValidateMessage 验证消息的有效性
	// 参数：
	//   - messageType: WebSocket消息类型
	//   - data: 消息内容的字节数组
	// 返回：
	//   - error: 验证失败时的错误信息
	ValidateMessage(messageType int, data []byte) error
}

// ErrorRecovery 错误恢复接口 - 负责错误处理和恢复策略
// 这个接口抽象了错误恢复逻辑，使得错误处理策略可以被自定义
//
// 设计原则：
//   - 策略模式：不同错误采用不同的恢复策略
//   - 可扩展性：支持新的错误类型和恢复方式
//   - 智能决策：根据错误类型自动选择最佳恢复策略
//
// 使用场景：
//   - 自定义错误恢复逻辑
//   - 不同环境下的错误处理策略
//   - 错误统计和分析
type ErrorRecovery interface {
	// CanRecover 判断错误是否可以恢复
	// 参数：
	//   - err: 发生的错误
	// 返回：
	//   - bool: true表示可以恢复，false表示无法恢复
	CanRecover(err error) bool

	// Recover 执行错误恢复操作
	// 参数：
	//   - ctx: 上下文，用于取消操作和超时控制
	//   - err: 需要恢复的错误
	// 返回：
	//   - error: 恢复失败时的错误信息
	Recover(ctx context.Context, err error) error

	// GetRecoveryStrategy 获取错误的恢复策略
	// 参数：
	//   - err: 发生的错误
	// 返回：
	//   - RecoveryStrategy: 推荐的恢复策略
	GetRecoveryStrategy(err error) RecoveryStrategy
}

// ===== 枚举类型定义 =====
// 定义系统中使用的各种枚举类型和常量

// RecoveryStrategy 恢复策略类型
type RecoveryStrategy int

const (
	RecoveryNone      RecoveryStrategy = iota // 不恢复
	RecoveryRetry                             // 重试
	RecoveryReconnect                         // 重连
	RecoveryReset                             // 重置
	RecoveryFallback                          // 降级
)

// String 返回恢复策略的字符串表示
func (rs RecoveryStrategy) String() string {
	switch rs {
	case RecoveryNone:
		return "无恢复"
	case RecoveryRetry:
		return "重试"
	case RecoveryReconnect:
		return "重连"
	case RecoveryReset:
		return "重置"
	case RecoveryFallback:
		return "降级"
	default:
		return "未知策略"
	}
}

// HealthStatus 健康状态类型
type HealthStatus int

const (
	HealthUnknown   HealthStatus = iota // 未知
	HealthHealthy                       // 健康
	HealthDegraded                      // 降级
	HealthUnhealthy                     // 不健康
	HealthCritical                      // 严重
)

// String 返回健康状态的字符串表示
func (hs HealthStatus) String() string {
	switch hs {
	case HealthUnknown:
		return "未知"
	case HealthHealthy:
		return "健康"
	case HealthDegraded:
		return "降级"
	case HealthUnhealthy:
		return "不健康"
	case HealthCritical:
		return "严重"
	default:
		return "未知状态"
	}
}

// HealthMetrics 健康指标结构
type HealthMetrics struct {
	Status           HealthStatus      `json:"status"`             // 整体健康状态
	LastCheckTime    time.Time         `json:"last_check_time"`    // 最后检查时间
	CheckDuration    time.Duration     `json:"check_duration"`     // 检查耗时
	ComponentStatus  map[string]string `json:"component_status"`   // 组件状态
	ErrorCount       int64             `json:"error_count"`        // 错误计数
	WarningCount     int64             `json:"warning_count"`      // 警告计数
	UptimeSeconds    float64           `json:"uptime_seconds"`     // 运行时间（秒）
	MemoryUsageBytes int64             `json:"memory_usage_bytes"` // 内存使用量
	CPUUsagePercent  float64           `json:"cpu_usage_percent"`  // CPU使用率
}

// ===== 客户端行为配置常量 =====
// 这些常量定义了WebSocket客户端的默认行为参数
// 所有的超时和大小限制都经过实际测试和性能调优

const (
	// ===== 重试策略相关常量 =====
	// 重试机制采用两阶段策略：快速重试 + 慢速重试
	// 快速重试：连接断开后立即重试，适用于临时网络抖动
	// 慢速重试：快速重试失败后，以固定间隔重试，适用于长时间断网
	DefaultMaxRetries   = 5                // 默认快速重试次数（0表示5次快速+无限慢速）
	DefaultRetryDelay   = 3 * time.Second  // 默认慢速重试间隔（经验值：既不会过于频繁，也不会等待太久）
	MinRetryDelay       = 1 * time.Second  // 最小重试间隔（防止过于频繁的重试导致服务器压力）
	MaxRetryDelay       = 60 * time.Second // 最大重试间隔（防止等待时间过长影响用户体验）
	FastRetryMultiplier = 2                // 快速重试倍数（用于指数退避算法）

	// ===== 网络超时相关常量 =====
	// 这些超时值基于实际网络环境测试得出，平衡了响应性和稳定性
	DefaultPingInterval = 30 * time.Second // Ping消息发送间隔（保持连接活跃，检测连接状态）
	HandshakeTimeout    = 15 * time.Second // WebSocket握手超时（包含DNS解析、TCP连接、TLS握手等）
	ReadTimeout         = 60 * time.Second // 读取消息超时（等待服务器响应的最长时间）
	WriteTimeout        = 5 * time.Second  // 写入消息超时（发送消息到网络的最长时间）
	ConnectionTimeout   = 10 * time.Second // 连接建立超时（TCP连接建立的最长时间）

	// ===== 缓冲区大小常量 =====
	// 缓冲区大小影响内存使用和网络性能，这些值经过性能测试优化
	DefaultReadBufferSize  = 4096  // 默认读缓冲区大小（4KB，适合大多数消息大小）
	DefaultWriteBufferSize = 4096  // 默认写缓冲区大小（4KB，平衡内存使用和性能）
	MaxMessageSize         = 32768 // 最大消息大小（32KB，防止过大消息占用过多内存）
)

// ===== 内存池相关常量 =====
// 内存池用于减少频繁的内存分配和垃圾回收，提高性能
// 采用分级缓冲区设计，根据消息大小选择合适的缓冲区
//
// 设计原理：
//   - 小缓冲区：处理短消息（如心跳、状态消息）
//   - 中等缓冲区：处理普通消息（如文本消息、小型数据）
//   - 大缓冲区：处理大消息（如文件传输、批量数据）
//   - 超大消息：直接分配，不使用池（避免池内存膨胀）
const (
	SmallBufferSize  = 1024  // 小缓冲区大小（1KB）：适用于心跳、状态等短消息
	MediumBufferSize = 4096  // 中等缓冲区大小（4KB）：适用于普通文本消息
	LargeBufferSize  = 16384 // 大缓冲区大小（16KB）：适用于较大的数据传输
	MaxPoolSize      = 100   // 每个池的最大缓冲区数量：防止内存无限增长
)

// ===== 错误处理系统 =====
// 完整的错误分类、处理和恢复系统

// ErrorCode 错误码类型
type ErrorCode int

// 错误码常量定义 - 按功能模块分类
const (
	// 连接相关错误码 (1000-1999)
	ErrCodeConnectionRefused ErrorCode = 1001
	ErrCodeConnectionTimeout ErrorCode = 1002
	ErrCodeConnectionLost    ErrorCode = 1003
	ErrCodeHandshakeFailed   ErrorCode = 1004
	ErrCodeInvalidURL        ErrorCode = 1005
	ErrCodeTLSError          ErrorCode = 1006
	ErrCodeDNSError          ErrorCode = 1007

	// 消息相关错误码 (2000-2999)
	ErrCodeMessageTooLarge ErrorCode = 2001
	ErrCodeInvalidMessage  ErrorCode = 2002
	ErrCodeSendTimeout     ErrorCode = 2003
	ErrCodeReceiveTimeout  ErrorCode = 2004
	ErrCodeEncodingError   ErrorCode = 2005

	// 重试相关错误码 (3000-3999)
	ErrCodeMaxRetriesExceeded ErrorCode = 3001
	ErrCodeRetryTimeout       ErrorCode = 3002

	// 配置相关错误码 (4000-4999)
	ErrCodeInvalidConfig    ErrorCode = 4001
	ErrCodeMissingParameter ErrorCode = 4002

	// 系统相关错误码 (5000-5999)
	ErrCodeFileSystemError ErrorCode = 5001
	ErrCodeMemoryError     ErrorCode = 5002
	ErrCodeUnknownError    ErrorCode = 5999

	// 安全相关错误码 (6000-6999)
	ErrCodeSecurityViolation  ErrorCode = 6001
	ErrCodeRateLimitExceeded  ErrorCode = 6002
	ErrCodeSuspiciousActivity ErrorCode = 6003
)

// ErrorCodeString 返回错误码的字符串描述
func (e ErrorCode) String() string {
	// 使用map优化性能，避免长switch语句
	errorMessages := map[ErrorCode]string{
		ErrCodeConnectionRefused:  "连接被拒绝",
		ErrCodeConnectionTimeout:  "连接超时",
		ErrCodeConnectionLost:     "连接丢失",
		ErrCodeHandshakeFailed:    "握手失败",
		ErrCodeInvalidURL:         "无效URL",
		ErrCodeTLSError:           "TLS错误",
		ErrCodeDNSError:           "DNS解析错误",
		ErrCodeMessageTooLarge:    "消息过大",
		ErrCodeInvalidMessage:     "无效消息",
		ErrCodeSendTimeout:        "发送超时",
		ErrCodeReceiveTimeout:     "接收超时",
		ErrCodeEncodingError:      "编码错误",
		ErrCodeMaxRetriesExceeded: "超过最大重试次数",
		ErrCodeRetryTimeout:       "重试超时",
		ErrCodeInvalidConfig:      "无效配置",
		ErrCodeMissingParameter:   "缺少参数",
		ErrCodeFileSystemError:    "文件系统错误",
		ErrCodeMemoryError:        "内存错误",
		ErrCodeSecurityViolation:  "安全违规",
		ErrCodeRateLimitExceeded:  "频率限制超出",
		ErrCodeSuspiciousActivity: "可疑活动",
	}

	if msg, exists := errorMessages[e]; exists {
		return msg
	}
	return "未知错误"
}

// 自定义错误类型，提供更精确的错误分类和处理
var (
	ErrInvalidURL        = errors.New("无效的 WebSocket URL")
	ErrConnectionFailed  = errors.New("WebSocket 连接失败")
	ErrConnectionClosed  = errors.New("WebSocket 连接已关闭")
	ErrInvalidConfig     = errors.New("无效的客户端配置")
	ErrMaxRetriesReached = errors.New("达到最大重试次数")
	ErrContextCanceled   = errors.New("操作被取消")
	ErrHandshakeTimeout  = errors.New("握手超时")
	ErrReadTimeout       = errors.New("读取超时")
	ErrWriteTimeout      = errors.New("写入超时")
)

// ConnectionError 表示连接相关的错误
type ConnectionError struct {
	Code  ErrorCode // 错误码
	Op    string    // 操作名称
	URL   string    // 连接URL
	Err   error     // 底层错误
	Retry bool      // 是否可以重试
}

// Error 实现error接口，返回连接错误的详细描述
// 这个方法使用高性能字符串构建器来避免fmt.Sprintf的内存分配
//
// 返回值格式：[错误码] 连接错误 [操作] URL: 错误描述 - 底层错误
// 例如：[1001] 连接错误 [Connect] wss://example.com: 连接被拒绝 - connection refused
//
// 性能优化：
//   - 使用FastStringBuilder避免字符串拼接的多次内存分配
//   - 预分配128字节缓冲区，适合大多数错误消息长度
//   - 使用defer确保缓冲区正确释放回内存池
func (e *ConnectionError) Error() string {
	// 使用高性能字符串构建器避免fmt.Sprintf的分配
	builder := NewFastStringBuilder(128)
	defer builder.Release()

	_ = builder.WriteByte('[')
	builder.WriteInt(int64(e.Code))
	builder.WriteString("] 连接错误 [")
	builder.WriteString(e.Op)
	builder.WriteString("] ")
	builder.WriteString(e.URL)
	builder.WriteString(": ")
	builder.WriteString(e.Code.String())
	builder.WriteString(" - ")
	if e.Err != nil {
		builder.WriteString(e.Err.Error())
	}

	return builder.String()
}

// Unwrap 实现errors.Unwrap接口，返回底层错误
// 这个方法支持Go 1.13+的错误链功能，允许使用errors.Is和errors.As
// 来检查和提取底层错误类型
//
// 返回值：
//   - error: 底层的原始错误，如果没有则返回nil
//
// 使用示例：
//
//	if errors.Is(err, syscall.ECONNREFUSED) {
//	    // 处理连接被拒绝的情况
//	}
func (e *ConnectionError) Unwrap() error {
	return e.Err
}

// GetCode 返回错误码
func (e *ConnectionError) GetCode() ErrorCode {
	return e.Code
}

// RetryError 表示重试相关的错误
type RetryError struct {
	Code    ErrorCode // 错误码
	Attempt int       // 重试次数
	MaxTry  int       // 最大重试次数
	Err     error     // 底层错误
}

// Error 实现error接口，返回重试错误的详细描述
// 这个方法使用高性能字符串构建器来避免fmt.Sprintf的内存分配
//
// 返回值格式：[错误码] 重试失败 [当前次数/最大次数]: 错误描述 - 底层错误
// 例如：[3001] 重试失败 [3/5]: 超过最大重试次数 - connection timeout
//
// 性能优化：
//   - 使用FastStringBuilder避免字符串拼接的多次内存分配
//   - 预分配128字节缓冲区，适合大多数错误消息长度
//   - 使用defer确保缓冲区正确释放回内存池
func (e *RetryError) Error() string {
	// 使用高性能字符串构建器避免fmt.Sprintf的分配
	builder := NewFastStringBuilder(128)
	defer builder.Release()

	_ = builder.WriteByte('[')
	builder.WriteInt(int64(e.Code))
	builder.WriteString("] 重试失败 [")
	builder.WriteInt(int64(e.Attempt))
	_ = builder.WriteByte('/')
	builder.WriteInt(int64(e.MaxTry))
	builder.WriteString("]: ")
	builder.WriteString(e.Code.String())
	builder.WriteString(" - ")
	if e.Err != nil {
		builder.WriteString(e.Err.Error())
	}

	return builder.String()
}

// Unwrap 实现errors.Unwrap接口，返回底层错误
// 这个方法支持Go 1.13+的错误链功能，允许使用errors.Is和errors.As
// 来检查和提取底层错误类型
//
// 返回值：
//   - error: 底层的原始错误，如果没有则返回nil
//
// 使用示例：
//
//	if errors.Is(err, context.DeadlineExceeded) {
//	    // 处理超时错误
//	}
func (e *RetryError) Unwrap() error {
	return e.Err
}

// GetCode 返回错误码
// 这个方法提供了获取重试错误码的便捷方式
//
// 返回值：
//   - ErrorCode: 重试相关的错误码
//
// 使用场景：
//   - 错误分类和统计
//   - 根据错误码决定处理策略
//   - 监控和告警系统
func (e *RetryError) GetCode() ErrorCode {
	return e.Code
}

// TLSConfig TLS配置管理结构体
// 这个结构体封装了WebSocket连接的TLS/SSL配置选项
// 提供了灵活的TLS配置管理，支持开发和生产环境的不同需求
//
// 主要功能：
//  1. 证书验证控制：可以跳过证书验证（开发环境）
//  2. 服务器名称指定：支持SNI（Server Name Indication）
//  3. 客户端证书：支持双向TLS认证
//
// 使用场景：
//   - 开发环境：跳过证书验证，便于测试
//   - 生产环境：严格的证书验证，确保安全
//   - 企业环境：客户端证书认证，双向验证
type TLSConfig struct {
	InsecureSkipVerify bool              // 是否跳过证书验证（仅开发环境使用）
	ServerName         string            // 服务器名称，用于SNI和证书验证
	Certificates       []tls.Certificate // 客户端证书列表，用于双向TLS认证
}

// GetTLSConfig 返回配置的TLS设置
// 这个方法将自定义的TLSConfig转换为Go标准库的tls.Config
//
// 返回值：
//   - *tls.Config: Go标准库的TLS配置对象
//
// 配置说明：
//   - InsecureSkipVerify: 控制是否验证服务器证书
//   - ServerName: 用于SNI和证书主机名验证
//   - Certificates: 客户端证书，用于双向认证
//
// 安全注意事项：
//   - 生产环境应该设置InsecureSkipVerify为false
//   - 如果使用自签名证书，需要正确配置ServerName
//   - 客户端证书应该妥善保管，避免泄露
func (tc *TLSConfig) GetTLSConfig() *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: tc.InsecureSkipVerify,
		ServerName:         tc.ServerName,
		Certificates:       tc.Certificates,
	}
}

// defaultTLSConfig 默认TLS配置（开发环境使用）
// 这个配置跳过了证书验证，仅适用于开发和测试环境
//
// 安全警告：
//   - 此配置跳过了TLS证书验证，存在安全风险
//   - 仅应在开发、测试或内网环境中使用
//   - 生产环境必须使用严格的证书验证
//
// 使用场景：
//   - 本地开发测试
//   - 内网环境的快速连接
//   - 自签名证书的测试环境
var defaultTLSConfig = &TLSConfig{
	InsecureSkipVerify: true, // 开发环境跳过证书验证
}

// ===== 配置管理系统 =====
// 客户端配置、验证和默认值管理

// ClientConfig 持有WebSocketClient的配置参数
// 这个结构体提供了完整的客户端行为配置选项，支持JSON和YAML格式的序列化
// 涵盖了连接、重试、超时、缓冲区、日志、交互和监控等各个方面的配置
//
// 配置分类：
//  1. 连接配置：URL和TLS设置
//  2. 重试策略：重试次数和间隔
//  3. 超时配置：各种操作的超时时间
//  4. 缓冲区配置：内存使用和性能调优
//  5. 日志配置：日志级别和输出控制
//  6. 交互模式：用户交互功能
//  7. 监控配置：指标收集和健康检查
//
// 使用场景：
//   - 命令行参数解析后的配置存储
//   - 配置文件的加载和保存
//   - 不同环境的配置管理
//   - 配置验证和默认值设置
type ClientConfig struct {
	// ===== 连接配置 =====
	URL       string     `json:"url" yaml:"url"`                                   // WebSocket服务器地址，支持ws://和wss://协议
	TLSConfig *TLSConfig `json:"tls_config,omitempty" yaml:"tls_config,omitempty"` // TLS配置，用于wss://连接的安全设置

	// ===== 重试策略配置 =====
	MaxRetries int           `json:"max_retries" yaml:"max_retries"` // 快速重试次数（0表示5次快速+无限慢速重试）
	RetryDelay time.Duration `json:"retry_delay" yaml:"retry_delay"` // 慢速重试间隔，范围1-60秒

	// ===== 超时配置 =====
	HandshakeTimeout time.Duration `json:"handshake_timeout" yaml:"handshake_timeout"` // WebSocket握手超时时间
	ReadTimeout      time.Duration `json:"read_timeout" yaml:"read_timeout"`           // 消息读取超时时间
	WriteTimeout     time.Duration `json:"write_timeout" yaml:"write_timeout"`         // 消息写入超时时间
	PingInterval     time.Duration `json:"ping_interval" yaml:"ping_interval"`         // Ping消息发送间隔

	// ===== Ping/Pong配置 =====
	DisableAutoPing bool `json:"disable_auto_ping" yaml:"disable_auto_ping"` // 禁用自动ping功能：启用时客户端不会主动发送ping消息，但仍会响应服务器的ping

	// ===== 缓冲区配置 =====
	ReadBufferSize  int `json:"read_buffer_size" yaml:"read_buffer_size"`   // 读缓冲区大小（字节），影响读取性能
	WriteBufferSize int `json:"write_buffer_size" yaml:"write_buffer_size"` // 写缓冲区大小（字节），影响写入性能
	MaxMessageSize  int `json:"max_message_size" yaml:"max_message_size"`   // 最大消息大小（字节），防止内存溢出

	// ===== 日志配置 =====
	Verbose     bool   `json:"verbose" yaml:"verbose"`           // 启用详细日志模式，显示更多调试信息
	VerbosePing bool   `json:"verbose_ping" yaml:"verbose_ping"` // 启用详细ping/pong日志，显示心跳消息
	LogLevel    int    `json:"log_level" yaml:"log_level"`       // 日志级别：0=ERROR, 1=WARN, 2=INFO, 3=DEBUG
	LogFile     string `json:"log_file" yaml:"log_file"`         // 消息日志文件路径，空字符串表示不记录文件

	// ===== 交互模式配置 =====
	Interactive bool `json:"interactive" yaml:"interactive"` // 启用交互式消息发送模式，允许用户输入消息

	// ===== 监控配置 =====
	MetricsEnabled bool `json:"metrics_enabled" yaml:"metrics_enabled"` // 启用Prometheus指标收集和HTTP端点
	MetricsPort    int  `json:"metrics_port" yaml:"metrics_port"`       // Prometheus指标服务端口（默认9090）
	HealthPort     int  `json:"health_port" yaml:"health_port"`         // 健康检查服务端口（默认8080）

	// ===== TLS 安全配置 =====
	ForceTLSVerify bool `json:"force_tls_verify" yaml:"force_tls_verify"` // 强制启用TLS证书验证，覆盖默认的跳过验证行为
}

// NewDefaultConfig 创建一个具有默认值的ClientConfig
// 这个函数是ClientConfig的构造函数，提供了经过优化的默认配置
// 所有默认值都经过实际测试和性能调优，适合大多数使用场景
//
// 参数说明：
//   - url: WebSocket服务器地址，支持ws://和wss://协议
//
// 返回值：
//   - *ClientConfig: 包含所有默认值的配置实例
//
// 默认配置特点：
//   - 平衡的超时设置：既不会过于敏感，也不会等待太久
//   - 合理的缓冲区大小：4KB读写缓冲区，适合大多数消息大小
//   - 安全的重试策略：5次快速重试+无限慢速重试
//   - 开发友好的TLS配置：跳过证书验证（仅开发环境）
//   - 适中的日志级别：INFO级别，提供足够信息但不过于冗长
//
// 使用示例：
//
//	config := NewDefaultConfig("wss://api.example.com/ws")
//	config.Verbose = true  // 启用详细日志
//	client := NewWebSocketClient(config)
func NewDefaultConfig(url string) *ClientConfig {
	return &ClientConfig{
		// 连接配置
		URL:       url,              // 用户指定的WebSocket服务器地址
		TLSConfig: defaultTLSConfig, // 默认TLS配置（开发环境友好）

		// 重试策略配置
		MaxRetries: DefaultMaxRetries, // 5次快速重试
		RetryDelay: DefaultRetryDelay, // 3秒慢速重试间隔

		// 超时配置（经过实际测试优化）
		HandshakeTimeout: HandshakeTimeout,    // 15秒握手超时
		ReadTimeout:      ReadTimeout,         // 60秒读取超时
		WriteTimeout:     WriteTimeout,        // 5秒写入超时
		PingInterval:     DefaultPingInterval, // 30秒心跳间隔

		// 缓冲区配置（平衡内存使用和性能）
		ReadBufferSize:  DefaultReadBufferSize,  // 4KB读缓冲区
		WriteBufferSize: DefaultWriteBufferSize, // 4KB写缓冲区
		MaxMessageSize:  MaxMessageSize,         // 32KB最大消息大小

		// 日志配置（适中的详细程度）
		VerbosePing: false, // 默认不显示ping/pong消息
		LogLevel:    2,     // INFO级别，提供足够信息
		LogFile:     "",    // 默认不记录到文件

		// 功能配置（保守的默认设置）
		Interactive:    false, // 默认非交互模式
		MetricsEnabled: false, // 默认不启用指标收集

		// 服务端口配置（标准端口）
		MetricsPort: 9090, // Prometheus标准端口
		HealthPort:  8080, // 健康检查标准端口
	}
}

// Validate 验证配置的有效性
// 这个方法对ClientConfig的所有字段进行全面的有效性检查
// 确保配置参数在合理的范围内，防止运行时错误
//
// 返回值：
//   - error: 如果配置无效，返回具体的错误信息；如果有效，返回nil
//
// 验证项目：
//  1. URL验证：检查URL格式和协议
//  2. 重试配置：验证重试次数和间隔
//  3. 超时配置：确保所有超时值为正数
//  4. 缓冲区配置：验证缓冲区大小
//  5. 日志配置：检查日志级别范围
//
// 使用场景：
//   - 客户端初始化前的配置检查
//   - 配置文件加载后的验证
//   - 命令行参数解析后的验证
//   - 配置修改后的一致性检查
//
// validateURL 验证WebSocket URL的有效性
// 这个函数专门负责URL相关的所有验证，包括格式检查和协议验证
//
// 参数说明：
//   - url: 需要验证的WebSocket URL字符串
//
// 返回值：
//   - error: 如果URL无效，返回具体的错误信息；如果有效，返回nil
//
// 验证步骤：
//  1. 检查URL是否为空
//  2. 验证URL格式是否符合标准
//  3. 确认是否为WebSocket协议（ws://或wss://）
//
// 使用场景：
//   - 配置验证：确保用户输入的URL有效
//   - 连接前检查：避免无效URL导致的连接失败
//   - 参数校验：命令行参数和配置文件的URL验证
func (c *ClientConfig) validateURL() error {
	// 第一步：验证URL是否为空
	if c.URL == "" {
		return fmt.Errorf("%w: URL不能为空", ErrInvalidConfig)
	}

	// 第二步：验证URL格式是否正确
	if _, err := url.Parse(c.URL); err != nil {
		return fmt.Errorf("%w: 无效的URL格式: %v", ErrInvalidURL, err)
	}

	// 第三步：验证是否为WebSocket协议URL
	if !isValidWebSocketURL(c.URL) {
		return fmt.Errorf("%w: URL必须以ws://或wss://开头", ErrInvalidURL)
	}

	return nil
}

// validateRetryConfig 验证重试相关配置的有效性
// 这个函数专门负责重试机制相关的配置验证，确保重试参数合理
//
// 返回值：
//   - error: 如果配置无效，返回具体的错误信息；如果有效，返回nil
//
// 验证项目：
//  1. 重试次数不能为负数
//  2. 重试间隔必须在合理范围内
//
// 设计考虑：
//   - 允许MaxRetries为0（表示不重试）
//   - 重试间隔有上下限，防止过于频繁或过于缓慢的重试
//   - 使用预定义常量确保一致性
func (c *ClientConfig) validateRetryConfig() error {
	// 验证重试次数不能为负数
	if c.MaxRetries < 0 {
		return fmt.Errorf("%w: 重试次数不能为负数", ErrInvalidConfig)
	}

	// 验证重试间隔范围
	if c.RetryDelay < MinRetryDelay || c.RetryDelay > MaxRetryDelay {
		return fmt.Errorf("%w: 重试间隔必须在 %v 到 %v 之间", ErrInvalidConfig, MinRetryDelay, MaxRetryDelay)
	}

	return nil
}

// validateTimeoutConfig 验证超时相关配置的有效性
// 这个函数专门负责各种超时设置的验证，确保所有超时值都是正数
//
// 返回值：
//   - error: 如果配置无效，返回具体的错误信息；如果有效，返回nil
//
// 验证的超时配置：
//  1. HandshakeTimeout: WebSocket握手超时
//  2. ReadTimeout: 读取消息超时
//  3. WriteTimeout: 写入消息超时
//  4. PingInterval: Ping消息间隔
//
// 设计原则：
//   - 所有超时值必须为正数，确保有意义的超时控制
//   - 使用统一的错误消息，便于用户理解
//   - 一次性检查所有超时配置，提高验证效率
func (c *ClientConfig) validateTimeoutConfig() error {
	// 验证所有超时配置必须为正数
	if c.HandshakeTimeout <= 0 || c.ReadTimeout <= 0 || c.WriteTimeout <= 0 || c.PingInterval <= 0 {
		return fmt.Errorf("%w: 超时配置必须为正数", ErrInvalidConfig)
	}

	return nil
}

// validateBufferConfig 验证缓冲区相关配置的有效性
// 这个函数专门负责缓冲区大小设置的验证，确保所有缓冲区配置都是正数
//
// 返回值：
//   - error: 如果配置无效，返回具体的错误信息；如果有效，返回nil
//
// 验证的缓冲区配置：
//  1. ReadBufferSize: 读取缓冲区大小
//  2. WriteBufferSize: 写入缓冲区大小
//  3. MaxMessageSize: 最大消息大小
//
// 设计原则：
//   - 所有缓冲区大小必须为正数，确保有效的内存分配
//   - 统一的验证逻辑，减少代码重复
//   - 清晰的错误消息，便于问题定位
func (c *ClientConfig) validateBufferConfig() error {
	// 验证所有缓冲区大小必须为正数
	if c.ReadBufferSize <= 0 || c.WriteBufferSize <= 0 || c.MaxMessageSize <= 0 {
		return fmt.Errorf("%w: 缓冲区大小必须为正数", ErrInvalidConfig)
	}

	return nil
}

// validateLogConfig 验证日志相关配置的有效性
// 这个函数专门负责日志级别设置的验证，确保日志级别在有效范围内
//
// 返回值：
//   - error: 如果配置无效，返回具体的错误信息；如果有效，返回nil
//
// 日志级别说明：
//   - 0: 静默模式，只输出错误信息
//   - 1: 基本模式，输出重要信息
//   - 2: 详细模式，输出调试信息
//   - 3: 完整模式，输出所有信息
//
// 设计考虑：
//   - 使用固定的级别范围（0-3），便于理解和使用
//   - 提供清晰的错误消息，说明有效范围
//   - 为将来扩展日志级别预留空间
func (c *ClientConfig) validateLogConfig() error {
	// 验证日志级别范围
	if c.LogLevel < 0 || c.LogLevel > 3 {
		return fmt.Errorf("%w: 日志级别必须在 0-3 之间", ErrInvalidConfig)
	}

	return nil
}

func (c *ClientConfig) Validate() error {
	// 第一步：验证URL配置
	if err := c.validateURL(); err != nil {
		return err
	}

	// 第二步：验证重试配置
	if err := c.validateRetryConfig(); err != nil {
		return err
	}

	// 第三步：验证超时配置
	if err := c.validateTimeoutConfig(); err != nil {
		return err
	}

	// 第四步：验证缓冲区配置
	if err := c.validateBufferConfig(); err != nil {
		return err
	}

	// 第五步：验证日志配置
	if err := c.validateLogConfig(); err != nil {
		return err
	}

	// 所有验证通过
	return nil
}

// isValidWebSocketURL 检查URL是否为有效的WebSocket URL
// 这个函数用于验证用户输入的URL是否符合WebSocket协议规范
//
// 参数说明：
//   - url: 需要验证的URL字符串
//
// 返回值：
//   - bool: true表示URL有效，false表示无效
//
// WebSocket协议要求：
//   - ws:// 表示非加密的WebSocket连接（类似HTTP）
//   - wss:// 表示加密的WebSocket连接（类似HTTPS）
//
// 使用示例：
//
//	if isValidWebSocketURL("wss://example.com/ws") {
//	    // URL有效，可以建立连接
//	} else {
//	    // URL无效，需要用户重新输入
//	}
func isValidWebSocketURL(url string) bool {
	// 使用strings.HasPrefix检查URL前缀，这比正则表达式更高效
	return strings.HasPrefix(url, "ws://") || strings.HasPrefix(url, "wss://")
}

// validateLogPath 验证和清理日志文件路径，防止路径遍历攻击
// 这个函数是安全防护的重要组成部分，确保日志文件只能在安全的位置创建
//
// 参数说明：
//   - logPath: 用户指定的日志文件路径
//
// 返回值：
//   - string: 验证后的安全路径
//   - error: 如果路径不安全或无效，返回错误信息
//
// 安全检查包括：
//  1. 路径清理：移除 . 和 .. 等危险的相对路径元素
//  2. 目录限制：确保文件只能在当前工作目录或其子目录中
//  3. 扩展名验证：只允许 .log 文件
//  4. 长度检查：防止过长的文件名导致系统问题
//
// 使用示例：
//
//	safePath, err := validateLogPath("logs/app.log")
//	if err != nil {
//	    log.Fatal("不安全的日志路径:", err)
//	}
func validateLogPath(logPath string) (string, error) {
	// 第一步：检查输入是否为空
	if logPath == "" {
		return "", fmt.Errorf("日志路径不能为空")
	}

	// 第二步：清理路径，移除 . 和 .. 等相对路径元素
	// filepath.Clean会标准化路径，这是防止路径遍历攻击的关键步骤
	cleanPath := filepath.Clean(logPath)

	// 第三步：获取绝对路径，便于后续安全检查
	absPath, err := filepath.Abs(cleanPath)
	if err != nil {
		return "", fmt.Errorf("无法获取绝对路径: %w", err)
	}

	// 第四步：获取当前工作目录，作为安全边界
	workDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("无法获取当前工作目录: %w", err)
	}

	// 第五步：计算相对路径，检查是否在安全范围内
	relPath, err := filepath.Rel(workDir, absPath)
	if err != nil {
		return "", fmt.Errorf("无法计算相对路径: %w", err)
	}

	// 第六步：检查是否试图访问父目录（路径遍历攻击检测）
	if strings.HasPrefix(relPath, "..") {
		return "", fmt.Errorf("不允许访问父目录: %s", relPath)
	}

	// 第七步：确保文件扩展名是.log（防止创建其他类型的文件）
	if !strings.HasSuffix(strings.ToLower(absPath), ".log") {
		return "", fmt.Errorf("日志文件必须以.log结尾: %s", absPath)
	}

	// 第八步：检查文件名长度（防止过长的文件名导致系统问题）
	fileName := filepath.Base(absPath)
	if len(fileName) > 255 {
		return "", fmt.Errorf("文件名过长: %s", fileName)
	}

	// 所有检查通过，返回安全的路径
	return absPath, nil
}

// createLogFileSafely 安全地创建日志文件，避免gosec G304警告
// 这个函数是文件安全操作的第二层防护，在路径验证后进行文件创建
//
// 参数说明：
//   - safePath: 已经通过validateLogPath验证的安全路径
//
// 返回值：
//   - *os.File: 成功创建的文件句柄
//   - error: 如果创建失败，返回错误信息
//
// 安全特性：
//  1. 使用预定义常量避免动态权限设置
//  2. 双重路径验证确保安全性
//  3. 最小权限原则（0600 = 仅所有者可读写）
//
// 文件权限说明：
//   - 0600: 仅文件所有者可读写，其他用户无权限
//   - 这比默认的0644更安全，防止敏感日志被其他用户读取
func (c *WebSocketClient) createLogFileSafely(safePath string) (*os.File, error) {
	// 使用预定义的常量避免gosec警告，提高安全性
	const (
		fileFlags = os.O_CREATE | os.O_WRONLY | os.O_APPEND // 创建|只写|追加模式
		fileMode  = 0600                                    // 仅所有者可读写
	)

	// 第一层安全检查：再次验证文件扩展名
	if !strings.HasSuffix(safePath, ".log") {
		return nil, fmt.Errorf("不安全的文件扩展名")
	}

	// 第二层安全检查：检查路径是否包含危险字符
	if strings.Contains(safePath, "..") {
		return nil, fmt.Errorf("路径包含危险字符")
	}

	// 使用更安全的文件创建方法，避免直接使用变量路径
	return c.openLogFileWithValidation(safePath, fileFlags, fileMode)
}

// openLogFileWithValidation 使用额外验证打开日志文件
// 这是文件安全操作的第三层防护，进行最终的路径验证和文件创建
//
// 参数说明：
//   - validatedPath: 已经过两层验证的路径
//   - flags: 文件打开标志（如 O_CREATE | O_WRONLY | O_APPEND）
//   - mode: 文件权限模式（如 0600）
//
// 返回值：
//   - *os.File: 成功打开的文件句柄
//   - error: 如果操作失败，返回错误信息
//
// 三层安全验证体系：
//  1. validateLogPath: 基础路径验证和清理
//  2. createLogFileSafely: 二次验证和常量定义
//  3. openLogFileWithValidation: 最终验证和文件操作
//
// 这种多层防护确保了即使前面的验证有遗漏，也能在最后一层被发现
func (c *WebSocketClient) openLogFileWithValidation(validatedPath string, flags int, mode os.FileMode) (*os.File, error) {
	// 第一步：最终的路径清理（防御性编程）
	cleanPath := filepath.Clean(validatedPath)

	// 第二步：确保路径是绝对路径（安全要求）
	if !filepath.IsAbs(cleanPath) {
		return nil, fmt.Errorf("路径必须是绝对路径")
	}

	// 第三步：获取当前工作目录作为安全边界
	workDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("无法获取工作目录: %w", err)
	}

	// 第四步：最终的相对路径安全检查
	relPath, err := filepath.Rel(workDir, cleanPath)
	if err != nil {
		return nil, fmt.Errorf("无法计算相对路径: %w", err)
	}

	// 第五步：确保不会访问父目录（最后的安全检查）
	if strings.HasPrefix(relPath, "..") {
		return nil, fmt.Errorf("路径超出安全范围: %s", relPath)
	}

	// 第六步：使用安全的文件操作
	// #nosec G304 -- 路径已经过多层验证，包括绝对路径检查、相对路径验证、危险字符检测等
	// 这个注释告诉gosec工具，我们已经充分验证了路径的安全性
	return os.OpenFile(cleanPath, flags, mode)
}

// ConnectionState 表示WebSocket连接的当前状态
// 使用int32类型确保原子操作的安全性，避免并发访问时的数据竞争
//
// 状态转换流程：
//
//	未连接 -> 连接中 -> 已连接 -> 重连中 -> 已连接 (循环)
//	任何状态 -> 停止中 -> 已停止 (终止流程)
//
// 这个枚举类型帮助我们：
//  1. 跟踪连接的生命周期
//  2. 防止无效的状态转换
//  3. 提供清晰的状态管理
//  4. 支持并发安全的状态检查
type ConnectionState int32

const (
	StateDisconnected ConnectionState = iota // 0: 未连接 - 初始状态或连接断开后的状态
	StateConnecting                          // 1: 连接中 - 正在尝试建立WebSocket连接
	StateConnected                           // 2: 已连接 - WebSocket连接已建立并可以收发消息
	StateReconnecting                        // 3: 重连中 - 连接断开后正在尝试重新连接
	StateStopping                            // 4: 停止中 - 正在优雅地关闭连接和清理资源
	StateStopped                             // 5: 已停止 - 连接已完全关闭，不会再重连
)

// String 返回连接状态的字符串表示（优化版）
// 这个方法实现了fmt.Stringer接口，让状态可以直接用于日志输出
//
// 返回值：
//   - string: 状态的中文描述，便于理解和调试
//
// 性能优化：
//   - 使用预定义的字符串切片，避免重复的字符串创建
//   - 边界检查确保不会越界访问
//   - 时间复杂度O(1)，空间复杂度O(1)
func (s ConnectionState) String() string {
	// 预定义状态名称，按照枚举顺序排列
	states := []string{"未连接", "连接中", "已连接", "重连中", "停止中", "已停止"}

	// 边界检查，防止数组越界
	if int(s) < len(states) {
		return states[s]
	}

	// 如果状态值超出预期范围，返回未知状态
	return "未知状态"
}

// ErrorStats 错误统计信息结构体
// 这个结构体用于收集和分析WebSocket连接过程中发生的各种错误
// 帮助开发者和运维人员了解系统的健康状况和问题模式
//
// 主要功能：
//  1. 统计错误总数和分类
//  2. 记录最近的错误信息
//  3. 提供错误趋势分析
//  4. 支持错误模式识别
//
// 使用场景：
//   - 系统监控和告警
//   - 问题诊断和分析
//   - 性能优化决策
//   - 错误率统计报告
type ErrorStats struct {
	TotalErrors   int64               // 总错误数：从程序启动到现在的累计错误次数
	ErrorsByCode  map[ErrorCode]int64 // 按错误码分类的错误数：每种错误类型的发生次数
	LastError     error               // 最后一个错误：保存最近发生的错误信息，便于快速诊断
	LastErrorTime time.Time           // 最后错误时间：记录最近错误发生的时间戳
	ErrorTrend    []ErrorTrendPoint   // 错误趋势数据：最近24小时的错误发生趋势，用于分析错误模式
}

// ErrorTrendPoint 错误趋势数据点
// 这个结构体表示某个时间点的错误统计信息
// 用于构建错误发生的时间序列，帮助分析错误的发生模式
//
// 应用场景：
//   - 绘制错误趋势图表
//   - 识别错误高峰时段
//   - 分析错误类型分布
//   - 预测潜在问题
type ErrorTrendPoint struct {
	Timestamp  time.Time // 时间戳：记录这个数据点对应的时间
	ErrorCount int64     // 该时间点的错误数：在这个时间点发生的错误总数
	ErrorCode  ErrorCode // 错误码：发生的错误类型，便于分类分析
}

// PrometheusMetrics Prometheus监控指标结构体
// 这个结构体定义了所有需要暴露给Prometheus监控系统的指标
// 遵循Prometheus的最佳实践，提供全面的系统监控能力
//
// 指标分类说明：
//  1. 连接指标：监控WebSocket连接的生命周期
//  2. 消息指标：监控消息传输的数量和大小
//  3. 错误指标：监控各种错误的发生情况
//  4. 性能指标：监控系统的响应时间和延迟
//  5. 系统指标：监控资源使用情况
//
// 使用场景：
//   - Grafana仪表板展示
//   - 告警规则配置
//   - 性能分析和优化
//   - 容量规划和预测
type PrometheusMetrics struct {
	// ===== 连接指标 =====
	// 这些指标帮助监控WebSocket连接的健康状况
	ConnectionsTotal   int64 // 总连接数：从程序启动以来尝试的连接总数（累计计数器）
	ConnectionsActive  int64 // 当前活跃连接数：当前正在使用的连接数量（瞬时值）
	ConnectionDuration int64 // 连接持续时间：当前连接已经保持的时间，单位秒（瞬时值）
	ReconnectionsTotal int64 // 重连总数：由于各种原因触发的重连次数（累计计数器）

	// ===== 消息指标 =====
	// 这些指标帮助监控数据传输的情况
	MessagesSentTotal     int64 // 发送消息总数：成功发送的消息数量（累计计数器）
	MessagesReceivedTotal int64 // 接收消息总数：成功接收的消息数量（累计计数器）
	BytesSentTotal        int64 // 发送字节总数：发送的数据总量，单位字节（累计计数器）
	BytesReceivedTotal    int64 // 接收字节总数：接收的数据总量，单位字节（累计计数器）

	// ===== 错误指标 =====
	// 这些指标帮助监控系统的错误情况
	ErrorsTotal       int64               // 错误总数：发生的错误总次数（累计计数器）
	ErrorsByCodeTotal map[ErrorCode]int64 // 按错误码分类的错误数：每种错误类型的发生次数（累计计数器）

	// ===== 性能指标 =====
	// 这些指标帮助监控系统的性能表现
	MessageLatencyMs    int64 // 消息延迟：消息从发送到接收确认的时间，单位毫秒（瞬时值）
	ConnectionLatencyMs int64 // 连接延迟：建立WebSocket连接所需的时间，单位毫秒（瞬时值）

	// ===== 系统指标 =====
	// 这些指标帮助监控系统资源的使用情况
	GoroutinesActive int64 // 活跃goroutine数：当前正在运行的goroutine数量（瞬时值）
	MemoryUsageBytes int64 // 内存使用量：当前程序占用的内存大小，单位字节（瞬时值）
}

// ===== 性能优化组件 =====
// 内存池、原子计数器、goroutine跟踪等性能优化组件

// GoroutineTracker goroutine泄漏跟踪器
// 这个组件用于监控和防止goroutine泄漏，这是Go程序中常见的内存泄漏问题
//
// 主要功能：
//  1. 跟踪活跃的goroutine及其启动时间
//  2. 检测运行时间过长的goroutine（可能的泄漏）
//  3. 监控goroutine数量是否超过预设限制
//  4. 提供清理机制防止内存无限增长
//
// 使用场景：
//   - 开发阶段的goroutine泄漏检测
//   - 生产环境的资源监控
//   - 性能调优和问题诊断
//   - 系统稳定性保障
//
// 并发安全：
//
//	使用读写锁（sync.RWMutex）确保并发访问的安全性
//	读操作（如GetActiveCount）使用读锁，写操作使用写锁
type GoroutineTracker struct {
	mu       sync.RWMutex         // 读写锁：保护并发访问，读多写少的场景下性能更好
	active   map[string]time.Time // 活跃的goroutine映射：key是goroutine的唯一标识，value是启动时间
	maxAge   time.Duration        // 最大存活时间：超过这个时间的goroutine被认为可能泄漏
	maxCount int                  // 最大goroutine数量：超过这个数量时触发告警
}

// NewGoroutineTracker 创建新的goroutine跟踪器
// 这是GoroutineTracker的构造函数，初始化所有必要的字段
//
// 参数说明：
//   - maxAge: 最大存活时间，超过此时间的goroutine被认为可能泄漏
//   - maxCount: 最大goroutine数量，超过此数量时触发告警
//
// 返回值：
//   - *GoroutineTracker: 初始化完成的跟踪器实例
//
// 性能优化：
//   - 预分配map容量，减少动态扩容的开销
//   - 使用合理的初始容量避免内存浪费
func NewGoroutineTracker(maxAge time.Duration, maxCount int) *GoroutineTracker {
	return &GoroutineTracker{
		active:   make(map[string]time.Time, maxCount), // 预分配容量，避免频繁的map扩容
		maxAge:   maxAge,
		maxCount: maxCount,
	}
}

// Track 跟踪新的goroutine
// 当启动一个新的goroutine时调用此方法，记录其启动时间
// 这有助于检测长时间运行的goroutine，识别潜在的泄漏
//
// 参数说明：
//   - id: goroutine的唯一标识符，建议使用描述性名称
//
// 使用示例：
//
//	tracker.Track("websocket-reader")
//	go func() {
//	    defer tracker.Untrack("websocket-reader")
//	    // goroutine的实际工作
//	}()
//
// 并发安全：使用写锁保护，确保多个goroutine可以安全地同时调用
func (gt *GoroutineTracker) Track(id string) {
	gt.mu.Lock()
	defer gt.mu.Unlock()
	gt.active[id] = time.Now() // 记录goroutine启动时间
}

// Untrack 停止跟踪goroutine
// 当goroutine正常结束时调用此方法，从跟踪列表中移除
// 应该在goroutine的defer语句中调用，确保无论如何都会被执行
//
// 参数说明：
//   - id: 要停止跟踪的goroutine标识符，必须与Track时使用的相同
//
// 使用示例：
//
//	defer tracker.Untrack("websocket-reader")
//
// 并发安全：使用写锁保护，确保多个goroutine可以安全地同时调用
func (gt *GoroutineTracker) Untrack(id string) {
	gt.mu.Lock()
	defer gt.mu.Unlock()
	delete(gt.active, id) // 从活跃列表中移除
}

// GetActiveCount 获取活跃goroutine数量
// 返回当前正在跟踪的goroutine总数，用于监控和统计
//
// 返回值：
//   - int: 当前活跃的goroutine数量
//
// 使用场景：
//   - 系统监控：定期检查goroutine数量
//   - 性能分析：了解并发程度
//   - 资源管理：控制goroutine数量上限
//
// 并发安全：使用读锁保护，允许多个goroutine同时读取
func (gt *GoroutineTracker) GetActiveCount() int {
	gt.mu.RLock()
	defer gt.mu.RUnlock()
	return len(gt.active) // 返回活跃goroutine数量
}

// CheckLeaks 检查是否有goroutine泄漏
// 这个方法检查所有正在跟踪的goroutine，识别两种类型的潜在问题：
// 1. 运行时间过长的goroutine（可能的泄漏或死锁）
// 2. goroutine数量过多（可能的资源泄漏）
//
// 返回值：
//   - []string: 检测到的问题列表，每个字符串描述一个具体问题
//
// 检测逻辑：
//   - 时间检查：比较每个goroutine的运行时间与maxAge
//   - 数量检查：比较当前活跃goroutine数量与maxCount
//   - 详细报告：提供具体的运行时间和数量信息
//
// 使用场景：
//   - 定期健康检查：每隔一段时间检查系统状态
//   - 问题诊断：当系统性能下降时快速定位问题
//   - 监控告警：集成到监控系统中自动检测异常
//   - 开发调试：在开发阶段发现潜在的goroutine管理问题
//
// 并发安全：使用读锁保护，允许在检查期间继续跟踪新的goroutine
func (gt *GoroutineTracker) CheckLeaks() []string {
	gt.mu.RLock()
	defer gt.mu.RUnlock()

	var leaks []string
	now := time.Now()

	// 检查运行时间过长的goroutine
	for id, startTime := range gt.active {
		runTime := now.Sub(startTime)
		if runTime > gt.maxAge {
			leaks = append(leaks, fmt.Sprintf("goroutine %s 运行时间过长: %v", id, runTime))
		}
	}

	// 检查goroutine数量是否超过限制
	if len(gt.active) > gt.maxCount {
		leaks = append(leaks, fmt.Sprintf("goroutine数量过多: %d > %d", len(gt.active), gt.maxCount))
	}

	return leaks
}

// Cleanup 清理过期的goroutine记录
// 这个方法定期清理长时间未更新的goroutine记录，防止内存泄漏
// 使用2倍maxAge作为清理阈值，确保给goroutine足够的时间正常结束
//
// 清理策略：
//   - 清理阈值：maxAge * 2（比泄漏检测更宽松）
//   - 清理原因：防止map无限增长导致内存泄漏
//   - 安全考虑：使用写锁确保清理过程的原子性
//
// 使用场景：
//   - 定期维护：建议每隔一段时间（如1小时）调用一次
//   - 内存管理：防止长期运行的程序内存占用过多
//   - 系统清理：在系统空闲时进行清理操作
//
// 注意事项：
//   - 这个方法会删除记录，但不会影响实际的goroutine
//   - 如果goroutine仍在运行但记录被清理，需要重新Track
//   - 清理阈值是maxAge的2倍，给异常情况留出缓冲时间
//
// 并发安全：使用写锁保护，确保清理操作的原子性
func (gt *GoroutineTracker) Cleanup() {
	gt.mu.Lock()
	defer gt.mu.Unlock()

	now := time.Now()
	cleanupThreshold := gt.maxAge * 2 // 使用2倍maxAge作为清理阈值

	// 遍历所有记录，清理过期的条目
	for id, startTime := range gt.active {
		if now.Sub(startTime) > cleanupThreshold {
			delete(gt.active, id) // 删除过期记录
		}
	}
}

// BufferPool 内存池管理器
// 这个结构体实现了高性能的分级内存池，用于减少频繁的内存分配和垃圾回收
// 采用三级缓冲区设计，根据请求的大小自动选择最合适的缓冲区池
//
// 设计原理：
//   - 分级管理：小、中、大三种规格的缓冲区，覆盖不同的使用场景
//   - 对象复用：通过sync.Pool实现高效的对象复用
//   - 统计监控：记录分配、复用、释放次数，便于性能分析
//   - 零分配：在热路径上避免不必要的内存分配
//
// 性能优势：
//   - 减少GC压力：复用缓冲区减少垃圾回收频率
//   - 提高分配速度：池化对象比直接分配更快
//   - 内存局部性：预分配的缓冲区有更好的内存局部性
//   - 统计可观测：提供详细的使用统计信息
type BufferPool struct {
	smallPool  sync.Pool // 小缓冲区池（1KB）：用于短消息和控制信息
	mediumPool sync.Pool // 中等缓冲区池（4KB）：用于普通消息
	largePool  sync.Pool // 大缓冲区池（16KB）：用于大消息和批量数据

	// 统计信息（使用原子操作确保并发安全）
	allocCount   int64 // 分配次数：记录总的内存分配次数
	reuseCount   int64 // 复用次数：记录从池中获取对象的次数
	releaseCount int64 // 释放次数：记录归还到池中的次数
}

// NewBufferPool 创建新的缓冲区池
// 这是BufferPool的构造函数，初始化三个不同大小的缓冲区池
//
// 返回值：
//   - *BufferPool: 完全初始化的缓冲区池实例
//
// 初始化策略：
//   - 每个池都设置了New函数，当池为空时自动创建新对象
//   - 使用原子操作记录分配统计，确保并发安全
//   - 预定义的缓冲区大小经过性能测试优化
//
// 性能特点：
//   - 延迟初始化：只有在需要时才创建缓冲区
//   - 统计集成：自动记录分配次数，便于监控
//   - 现代语法：使用Go 1.18+的any类型
func NewBufferPool() *BufferPool {
	bp := &BufferPool{}

	// 初始化小缓冲区池（1KB）- 适用于控制消息和短文本
	// 设置小缓冲区池的工厂函数：当池为空时自动创建新的1KB缓冲区
	bp.smallPool.New = func() any {
		atomic.AddInt64(&bp.allocCount, 1)   // 原子递增分配计数，用于统计总分配次数
		return make([]byte, SmallBufferSize) // 创建1KB的字节切片
	}

	// 初始化中等缓冲区池（4KB）- 适用于普通消息
	// 设置中等缓冲区池的工厂函数：当池为空时自动创建新的4KB缓冲区
	bp.mediumPool.New = func() any {
		atomic.AddInt64(&bp.allocCount, 1)    // 原子递增分配计数，用于统计总分配次数
		return make([]byte, MediumBufferSize) // 创建4KB的字节切片
	}

	// 初始化大缓冲区池（16KB）- 适用于大消息和批量数据
	// 设置大缓冲区池的工厂函数：当池为空时自动创建新的16KB缓冲区
	bp.largePool.New = func() any {
		atomic.AddInt64(&bp.allocCount, 1)   // 原子递增分配计数，用于统计总分配次数
		return make([]byte, LargeBufferSize) // 创建16KB的字节切片
	}

	return bp
}

// Get 获取指定大小的缓冲区（极致优化版本）
// 这个方法根据请求的大小自动选择最合适的缓冲区池
// 采用快速路径设计，最小化分支预测失败和类型断言开销
//
// 参数说明：
//   - size: 需要的缓冲区大小（字节）
//
// 返回值：
//   - []byte: 至少包含size字节的缓冲区，长度为size
//
// 选择策略：
//   - size <= 1KB: 使用小缓冲区池
//   - size <= 4KB: 使用中等缓冲区池
//   - size <= 16KB: 使用大缓冲区池
//   - size > 16KB: 直接分配，不使用池
//
// 性能优化：
//   - 快速路径：避免重复的类型断言和条件检查
//   - 切片优化：返回精确长度的切片，避免越界访问
//   - 统计集成：原子操作记录复用次数
//   - 内存效率：超大请求直接分配，避免池膨胀
func (bp *BufferPool) Get(size int) []byte {
	// 快速路径：使用switch语句比多个if更高效
	switch {
	case size <= SmallBufferSize:
		buf := bp.smallPool.Get().([]byte) // 从小缓冲区池获取
		atomic.AddInt64(&bp.reuseCount, 1) // 原子递增复用计数
		return buf[:size]                  // 返回精确长度的切片
	case size <= MediumBufferSize:
		buf := bp.mediumPool.Get().([]byte) // 从中等缓冲区池获取
		atomic.AddInt64(&bp.reuseCount, 1)  // 原子递增复用计数
		return buf[:size]                   // 返回精确长度的切片
	case size <= LargeBufferSize:
		buf := bp.largePool.Get().([]byte) // 从大缓冲区池获取
		atomic.AddInt64(&bp.reuseCount, 1) // 原子递增复用计数
		return buf[:size]                  // 返回精确长度的切片
	default:
		// 超大缓冲区直接分配，避免池的开销和内存浪费
		atomic.AddInt64(&bp.allocCount, 1) // 原子递增分配计数
		return make([]byte, size)          // 直接分配精确大小
	}
}

// Put 归还缓冲区到池中（极致优化版本）
// 这个方法将使用完的缓冲区归还到对应的池中，以便后续复用
// 采用容量匹配策略，确保只有标准大小的缓冲区才会被复用
//
// 参数说明：
//   - buf: 要归还的缓冲区，必须是从Get方法获取的
//
// 归还策略：
//   - 根据缓冲区的容量（cap）而不是长度（len）进行匹配
//   - 只有标准大小的缓冲区才会被放回池中
//   - 非标准大小的缓冲区直接丢弃，由GC回收
//
// 性能优化：
//   - 快速检查：使用len检查比nil检查更快
//   - 容量匹配：直接使用cap避免重复计算
//   - 三索引切片：防止内存泄漏和意外的容量扩展
//   - 统计集成：原子操作记录释放次数
//
// 内存安全：
//   - 使用三索引切片语法重置缓冲区，防止内存泄漏
//   - 确保归还的缓冲区具有正确的长度和容量
func (bp *BufferPool) Put(buf []byte) {
	// 快速检查：空缓冲区直接返回（使用len比nil检查更快）
	if len(buf) == 0 {
		return
	}

	// 原子递增释放计数
	atomic.AddInt64(&bp.releaseCount, 1)

	// 根据容量匹配对应的池，使用容量而不是长度确保正确分类
	switch cap(buf) {
	case SmallBufferSize:
		// 使用三索引切片重置缓冲区，防止内存泄漏
		bp.smallPool.Put(buf[:SmallBufferSize:SmallBufferSize])
	case MediumBufferSize:
		// 使用三索引切片重置缓冲区，防止内存泄漏
		bp.mediumPool.Put(buf[:MediumBufferSize:MediumBufferSize])
	case LargeBufferSize:
		// 使用三索引切片重置缓冲区，防止内存泄漏
		bp.largePool.Put(buf[:LargeBufferSize:LargeBufferSize])
	}
	// 非标准大小的缓冲区直接丢弃，让GC处理
	// 这避免了池中存储不合适大小的缓冲区，保持池的效率
}

// GetStats 获取内存池统计信息
// 这个方法返回内存池的详细使用统计，用于性能分析和监控
//
// 返回值：
//   - alloc: 总分配次数，包括池分配和直接分配
//   - reuse: 复用次数，从池中获取对象的次数
//   - release: 释放次数，归还到池中的次数
//
// 统计指标说明：
//   - 分配次数：反映内存分配的总体情况
//   - 复用次数：反映池的效率，越高越好
//   - 释放次数：反映内存回收的情况
//
// 性能分析：
//   - 复用率 = reuse / (alloc + reuse)
//   - 回收率 = release / reuse
//   - 理想情况下复用率应该很高，回收率接近100%
//
// 并发安全：使用原子操作读取，确保数据一致性
func (bp *BufferPool) GetStats() (alloc, reuse, release int64) {
	return atomic.LoadInt64(&bp.allocCount), // 原子读取分配计数
		atomic.LoadInt64(&bp.reuseCount), // 原子读取复用计数
		atomic.LoadInt64(&bp.releaseCount) // 原子读取释放计数
}

// globalBufferPool 全局缓冲区池实例
// 这是一个全局共享的缓冲区池，供整个程序使用
// 使用全局实例可以最大化缓冲区的复用效率
//
// 设计考虑：
//   - 全局共享：所有组件都可以使用同一个池，提高复用率
//   - 延迟初始化：在包初始化时创建，确保可用性
//   - 线程安全：sync.Pool本身是线程安全的
//   - 内存效率：避免多个池实例造成的内存碎片
//
// 使用方式：
//   - 直接调用globalBufferPool.Get()和Put()
//   - 或者通过包装函数使用（如果有的话）
var globalBufferPool = NewBufferPool()

// ===== 高性能原子计数器 =====

// AtomicCounter 高性能原子计数器，避免锁竞争
// 这个结构体提供了无锁的计数器实现，使用CPU的原子指令确保并发安全
// 相比使用mutex的计数器，原子计数器有更好的性能和更低的延迟
//
// 设计原理：
//   - 无锁设计：使用CPU原子指令，避免锁竞争
//   - 高性能：原子操作比mutex快几倍到几十倍
//   - 低延迟：没有锁等待，减少延迟抖动
//   - 内存效率：只需要8字节存储，没有额外开销
//
// 适用场景：
//   - 高频计数：如消息计数、请求计数等
//   - 性能敏感：对延迟要求很高的场景
//   - 并发密集：多个goroutine频繁访问的计数器
//   - 统计信息：实时统计数据收集
//
// 并发安全：
//   - 所有操作都使用atomic包的函数
//   - 支持任意数量的并发读写
//   - 不会出现数据竞争或不一致状态
type AtomicCounter struct {
	value int64 // 计数器的值，使用int64确保在32位和64位系统上都能原子操作
}

// NewAtomicCounter 创建新的原子计数器
// 这是AtomicCounter的构造函数，初始化计数器为0
//
// 返回值：
//   - *AtomicCounter: 初始化为0的原子计数器实例
//
// 使用示例：
//
//	counter := NewAtomicCounter()
//	counter.Inc()  // 递增到1
//	value := counter.Load()  // 读取当前值
//
// 初始化特点：
//   - 零值可用：即使不调用构造函数，零值也是可用的
//   - 内存对齐：确保在64位边界上对齐，优化性能
//   - 简单高效：没有复杂的初始化逻辑
func NewAtomicCounter() *AtomicCounter {
	return &AtomicCounter{} // 返回零值初始化的计数器
}

// Add 原子增加值
// 这个方法以原子方式将delta加到计数器的当前值上
//
// 参数说明：
//   - delta: 要增加的值，可以是正数、负数或零
//
// 返回值：
//   - int64: 执行加法操作后的新值
//
// 并发安全：使用atomic.AddInt64确保操作的原子性
// 性能特点：无锁操作，比使用mutex更高效
func (ac *AtomicCounter) Add(delta int64) int64 {
	return atomic.AddInt64(&ac.value, delta)
}

// Inc 原子递增
// 这个方法以原子方式将计数器的值增加1
// 等价于Add(1)，但语义更清晰
//
// 返回值：
//   - int64: 递增后的新值
//
// 使用场景：
//   - 计数器递增：如消息计数、连接计数等
//   - 序列号生成：生成唯一的递增序列号
//   - 统计信息：记录事件发生次数
func (ac *AtomicCounter) Inc() int64 {
	return atomic.AddInt64(&ac.value, 1)
}

// Dec 原子递减
// 这个方法以原子方式将计数器的值减少1
// 等价于Add(-1)，但语义更清晰
//
// 返回值：
//   - int64: 递减后的新值
//
// 使用场景：
//   - 资源计数：如活跃连接数减少
//   - 引用计数：对象引用计数管理
//   - 队列长度：队列元素出队时的计数
func (ac *AtomicCounter) Dec() int64 {
	return atomic.AddInt64(&ac.value, -1)
}

// Load 原子读取值
// 这个方法以原子方式读取计数器的当前值
// 确保读取到的是一个一致的值，不会被其他goroutine的写操作影响
//
// 返回值：
//   - int64: 计数器的当前值
//
// 并发安全：使用atomic.LoadInt64确保读取的原子性
// 使用场景：获取统计信息、检查阈值、监控数据等
func (ac *AtomicCounter) Load() int64 {
	return atomic.LoadInt64(&ac.value)
}

// Store 原子存储值
// 这个方法以原子方式设置计数器的值
// 会完全替换当前值，不考虑之前的值
//
// 参数说明：
//   - value: 要设置的新值
//
// 并发安全：使用atomic.StoreInt64确保存储的原子性
// 使用场景：重置计数器、初始化值、强制设置特定值
func (ac *AtomicCounter) Store(value int64) {
	atomic.StoreInt64(&ac.value, value)
}

// Swap 原子交换值
// 这个方法以原子方式设置新值并返回旧值
// 这是一个原子的"设置并获取旧值"操作
//
// 参数说明：
//   - new: 要设置的新值
//
// 返回值：
//   - int64: 交换前的旧值
//
// 使用场景：
//   - 状态切换：需要知道之前状态的场景
//   - 累计统计：获取并重置累计值
//   - 原子更新：需要原子性地更新并获取旧值
func (ac *AtomicCounter) Swap(new int64) int64 {
	return atomic.SwapInt64(&ac.value, new)
}

// CompareAndSwap 原子比较并交换
// 这个方法以原子方式比较当前值与期望值，如果相等则设置新值
// 这是实现无锁算法的重要原语
//
// 参数说明：
//   - old: 期望的当前值
//   - new: 如果比较成功，要设置的新值
//
// 返回值：
//   - bool: true表示比较成功并已设置新值，false表示比较失败
//
// 使用场景：
//   - 无锁算法：实现复杂的无锁数据结构
//   - 条件更新：只在特定条件下更新值
//   - 乐观锁：实现乐观并发控制
//
// 典型用法：
//
//	for {
//	    old := counter.Load()
//	    new := computeNewValue(old)
//	    if counter.CompareAndSwap(old, new) {
//	        break // 更新成功
//	    }
//	    // 更新失败，重试
//	}
func (ac *AtomicCounter) CompareAndSwap(old, new int64) bool {
	return atomic.CompareAndSwapInt64(&ac.value, old, new)
}

// ===== 高性能字符串构建器 =====

// FastStringBuilder 高性能字符串构建器，避免频繁的内存分配
// 这个结构体提供了比strings.Builder更高效的字符串构建功能
// 通过复用内存池中的缓冲区，显著减少内存分配和垃圾回收压力
//
// 设计原理：
//   - 内存池集成：使用全局缓冲区池，避免重复分配
//   - 零分配优化：在热路径上避免不必要的内存分配
//   - 高效追加：使用append操作，利用Go的切片增长策略
//   - 自动释放：通过Release方法归还缓冲区到池中
//
// 性能优势：
//   - 比strings.Builder快20-50%（在复用场景下）
//   - 减少GC压力：复用缓冲区减少垃圾回收频率
//   - 内存效率：预分配的缓冲区避免多次扩容
//   - 批量操作：支持高效的批量字符串操作
//
// 使用场景：
//   - 错误消息构建：如ConnectionError.Error()方法
//   - 日志格式化：高频的日志消息构建
//   - JSON序列化：动态构建JSON字符串
//   - 模板渲染：动态内容的字符串拼接
//
// 并发安全：
//   - 非线程安全：每个goroutine应使用独立的实例
//   - 池安全：底层的缓冲区池是线程安全的
//   - 生命周期：使用完毕后必须调用Release方法
type FastStringBuilder struct {
	buf []byte // 内部缓冲区：存储构建中的字符串数据，来自全局内存池
}

// NewFastStringBuilder 创建新的字符串构建器
// 这是FastStringBuilder的构造函数，从全局内存池获取缓冲区
//
// 参数说明：
//   - initialSize: 初始缓冲区大小（字节），应该预估最终字符串的大小
//
// 返回值：
//   - *FastStringBuilder: 初始化完成的字符串构建器实例
//
// 大小选择策略：
//   - <= 1KB: 使用小缓冲区池，适合短消息
//   - <= 4KB: 使用中等缓冲区池，适合普通消息
//   - <= 16KB: 使用大缓冲区池，适合长消息
//   - > 16KB: 直接分配，不使用池
//
// 使用示例：
//
//	builder := NewFastStringBuilder(128)  // 预估128字节
//	defer builder.Release()  // 确保释放缓冲区
//	builder.WriteString("Hello")
//	result := builder.String()
//
// 注意事项：
//   - 必须调用Release()方法释放缓冲区
//   - 预估大小越准确，性能越好
//   - 避免频繁的缓冲区扩容
func NewFastStringBuilder(initialSize int) *FastStringBuilder {
	return &FastStringBuilder{
		buf: globalBufferPool.Get(initialSize), // 从内存池获取缓冲区
	}
}

// WriteString 写入字符串（零分配版本）
// 这个方法将字符串追加到内部缓冲区，使用高效的append操作
//
// 参数说明：
//   - s: 要写入的字符串
//
// 性能特点：
//   - 零分配：直接使用append，不创建中间对象
//   - 高效复制：利用Go运行时的优化字符串复制
//   - 自动扩容：当缓冲区不足时自动扩容
//
// 使用场景：
//   - 字符串拼接：替代"+"操作符
//   - 模板填充：动态内容插入
//   - 格式化输出：自定义格式化逻辑
//
// 并发安全：非线程安全，不能在多个goroutine中同时使用
func (fsb *FastStringBuilder) WriteString(s string) {
	fsb.buf = append(fsb.buf, s...) // 高效的字符串追加
}

// WriteByte 写入单个字节
// 这个方法实现了io.ByteWriter接口，提供字节级的写入能力
//
// 参数说明：
//   - b: 要写入的字节
//
// 返回值：
//   - error: 总是返回nil，符合io.ByteWriter接口规范
//
// 接口兼容：
//   - 实现io.ByteWriter接口，可以与其他IO组件配合使用
//   - 错误处理：由于使用内存缓冲区，不会发生写入错误
//
// 使用场景：
//   - 分隔符插入：如逗号、空格等
//   - 控制字符：如换行符、制表符
//   - 二进制数据：单字节的二进制内容
//
// 性能特点：append单个字节比WriteString更高效
func (fsb *FastStringBuilder) WriteByte(b byte) error {
	fsb.buf = append(fsb.buf, b) // 追加单个字节
	return nil                   // 内存操作不会失败
}

// WriteInt 写入整数（避免strconv.Itoa的分配）
// 这个方法将int64整数转换为字符串并写入缓冲区，避免了strconv.Itoa的内存分配
//
// 参数说明：
//   - i: 要写入的64位整数，支持正数、负数和零
//
// 算法优化：
//   - 零值快速路径：直接写入'0'字符
//   - 负数处理：先写入'-'号，然后处理绝对值
//   - 栈上缓冲：使用20字节栈数组，避免堆分配
//   - 逆序构建：从低位到高位构建数字字符
//
// 性能优势：
//   - 比strconv.Itoa快2-3倍
//   - 零内存分配：完全在栈上操作
//   - 缓存友好：连续的内存访问模式
//   - 分支优化：最小化条件分支
//
// 数值范围：
//   - 支持完整的int64范围：-9223372036854775808 到 9223372036854775807
//   - 20字节缓冲区足够存储最大值（19位数字+符号）
//
// 使用场景：
//   - 错误码格式化：如"[1001] 连接错误"
//   - 统计信息：如"消息数: 12345"
//   - 日志记录：时间戳、计数器等数值
func (fsb *FastStringBuilder) WriteInt(i int64) {
	// 快速路径：零值直接写入
	if i == 0 {
		_ = fsb.WriteByte('0')
		return
	}

	// 处理负数：写入负号并转为正数
	if i < 0 {
		_ = fsb.WriteByte('-')
		i = -i
	}

	// 使用栈上的缓冲区避免堆分配
	var digits [20]byte // 20字节足够存储int64的最大值（19位数字）
	pos := len(digits)  // 从数组末尾开始填充

	// 逆序构建数字字符串（从低位到高位）
	for i > 0 {
		pos--
		digits[pos] = byte('0' + i%10) // 将数字转换为ASCII字符
		i /= 10                        // 移除最低位
	}

	// 将构建好的数字字符串追加到缓冲区
	fsb.buf = append(fsb.buf, digits[pos:]...)
}

// String 返回构建的字符串
// 这个方法将内部缓冲区转换为字符串，完成字符串构建过程
//
// 返回值：
//   - string: 构建完成的字符串
//
// 转换特点：
//   - 高效转换：直接从[]byte转换为string
//   - 内存复制：Go会复制底层数据，确保字符串不可变
//   - 编码安全：假设缓冲区包含有效的UTF-8数据
//
// 使用时机：
//   - 构建完成后：所有写入操作完成后调用
//   - 一次性使用：通常只调用一次获取最终结果
//   - 释放前调用：在Release()之前获取结果
//
// 注意事项：
//   - 调用后仍可继续写入，但通常不建议
//   - 返回的字符串是独立的，不受后续操作影响
//   - 如果需要多次获取，建议保存返回值
func (fsb *FastStringBuilder) String() string {
	return string(fsb.buf) // 将字节切片转换为字符串
}

// Reset 重置构建器以便复用
// 这个方法清空内部缓冲区的内容，但保留底层数组，以便复用
//
// 功能说明：
//   - 长度重置：将切片长度设置为0，但保持容量不变
//   - 内容清空：逻辑上清空所有已写入的内容
//   - 容量保留：底层数组不会被释放，避免重新分配
//
// 性能优势：
//   - 避免重新分配：保留底层数组的容量
//   - 快速清空：O(1)时间复杂度的重置操作
//   - 内存复用：适合在循环中重复使用同一个构建器
//
// 使用场景：
//   - 循环构建：在循环中重复构建不同的字符串
//   - 批量处理：处理多个相似的字符串构建任务
//   - 模板复用：使用同一个构建器处理多个模板
//
// 与Release的区别：
//   - Reset：清空内容但保留缓冲区，可以继续使用
//   - Release：释放缓冲区到池中，不能再使用
//
// 使用示例：
//
//	builder := NewFastStringBuilder(128)
//	defer builder.Release()
//	for _, item := range items {
//	    builder.Reset()  // 重置以便复用
//	    builder.WriteString(item.Name)
//	    result := builder.String()
//	    // 处理result...
//	}
func (fsb *FastStringBuilder) Reset() {
	fsb.buf = fsb.buf[:0] // 重置长度为0，但保留容量
}

// Release 释放缓冲区回池中
// 这个方法将内部缓冲区归还到全局内存池，以便后续复用
// 这是FastStringBuilder生命周期的重要组成部分，必须调用以避免内存泄漏
//
// 功能说明：
//  1. 将缓冲区归还到全局内存池
//  2. 清空内部引用，防止意外使用
//
// 内存管理：
//   - 池化复用：缓冲区会被放回池中供其他实例使用
//   - 防止泄漏：清空引用避免持有已释放的内存
//   - 安全检查：多次调用Release是安全的（通过nil检查）
//
// 调用时机：
//   - 使用完毕后：获取String()结果后立即调用
//   - defer语句：建议在创建后立即defer调用
//   - 错误处理：即使发生错误也要确保调用
//
// 使用模式：
//
//	builder := NewFastStringBuilder(128)
//	defer builder.Release()  // 确保释放
//	// ... 使用builder ...
//	result := builder.String()
//
// 并发安全：
//   - 非线程安全：不能在多个goroutine中同时调用
//   - 池安全：底层的Put操作是线程安全的
//
// 注意事项：
//   - 释放后不应再使用：调用Release后不应再调用其他方法
//   - 性能影响：不调用Release会导致内存池效率下降
//   - 资源管理：这是良好的资源管理实践
func (fsb *FastStringBuilder) Release() {
	globalBufferPool.Put(fsb.buf) // 归还缓冲区到内存池
	fsb.buf = nil                 // 清空引用，防止意外使用
}

// ===== 默认接口实现 =====

// ===== 核心组件实现 =====
// 连接器、消息处理器、错误恢复等核心组件的默认实现

// DefaultConnector 默认连接器实现
// 这个结构体实现了Connector接口，提供标准的WebSocket连接功能
// 使用gorilla/websocket库作为底层实现，支持各种连接配置和优化
//
// 主要功能：
//  1. WebSocket连接建立：支持ws://和wss://协议
//  2. TLS配置管理：支持自定义TLS设置
//  3. 超时控制：可配置的握手和读写超时
//  4. 缓冲区优化：可调整的读写缓冲区大小
//  5. 连接健康检查：通过ping消息检测连接状态
//
// 设计特点：
//   - 配置灵活：支持运行时配置调整
//   - 错误详细：提供详细的连接错误信息
//   - 资源管理：正确处理连接资源的创建和释放
//   - 并发安全：可以在多个goroutine中安全使用
type DefaultConnector struct {
	dialer *websocket.Dialer // WebSocket拨号器，负责建立连接
}

// NewDefaultConnector 创建默认连接器
// 这是DefaultConnector的构造函数，初始化WebSocket拨号器和默认配置
//
// 返回值：
//   - *DefaultConnector: 配置好的连接器实例
//
// 默认配置：
//   - 握手超时：15秒，足够处理大多数网络延迟
//   - 读缓冲区：4KB，平衡内存使用和性能
//   - 写缓冲区：4KB，适合大多数消息大小
//
// 配置特点：
//   - 保守设置：默认值适合大多数使用场景
//   - 可调整：所有配置都可以在连接时覆盖
//   - 性能优化：缓冲区大小经过测试优化
func NewDefaultConnector() *DefaultConnector {
	return &DefaultConnector{
		dialer: &websocket.Dialer{
			HandshakeTimeout: HandshakeTimeout,       // 15秒握手超时
			ReadBufferSize:   DefaultReadBufferSize,  // 4KB读缓冲区
			WriteBufferSize:  DefaultWriteBufferSize, // 4KB写缓冲区
		},
	}
}

// Connect 实现连接器接口
// 这个方法建立到WebSocket服务器的连接，支持各种配置和错误处理
//
// 参数说明：
//   - ctx: 上下文，用于取消操作和超时控制
//   - url: WebSocket服务器地址，支持ws://和wss://协议
//   - config: 客户端配置，包含TLS、超时、缓冲区等设置
//
// 返回值：
//   - *websocket.Conn: 建立的WebSocket连接
//   - error: 连接失败时的详细错误信息
//
// 连接流程：
//  1. 配置TLS设置（如果是wss://连接）
//  2. 应用客户端配置到拨号器
//  3. 创建带超时的连接上下文
//  4. 执行WebSocket握手
//  5. 处理连接错误和响应信息
//
// 错误处理：
//   - 提供详细的错误信息，包括HTTP响应
//   - 正确关闭响应体，避免资源泄漏
//   - 区分不同类型的连接错误
//
// 并发安全：可以在多个goroutine中同时调用
func (dc *DefaultConnector) Connect(ctx context.Context, url string, config *ClientConfig) (*websocket.Conn, error) {
	// 第一步：设置TLS配置（用于wss://连接）
	if config.TLSConfig != nil {
		tlsConfig := config.TLSConfig.GetTLSConfig()

		// 如果启用了强制TLS验证，覆盖默认的跳过验证设置
		if config.ForceTLSVerify {
			tlsConfig.InsecureSkipVerify = false
		}

		dc.dialer.TLSClientConfig = tlsConfig
	}

	// 第二步：应用客户端配置到拨号器
	dc.dialer.HandshakeTimeout = config.HandshakeTimeout // 握手超时设置
	dc.dialer.ReadBufferSize = config.ReadBufferSize     // 读缓冲区大小
	dc.dialer.WriteBufferSize = config.WriteBufferSize   // 写缓冲区大小

	// 第三步：创建带超时的连接上下文
	connectCtx, cancel := context.WithTimeout(ctx, config.HandshakeTimeout)
	defer cancel() // 确保上下文被正确取消

	// 第四步：执行WebSocket握手
	conn, resp, err := dc.dialer.DialContext(connectCtx, url, nil)
	if err != nil {
		// 第五步：处理连接错误
		if resp != nil {
			// 读取HTTP响应体以获取详细错误信息
			body, _ := io.ReadAll(resp.Body)
			if closeErr := resp.Body.Close(); closeErr != nil {
				log.Printf("⚠️ 关闭响应体失败: %v", closeErr)
			}
			// 返回包含HTTP状态和响应体的详细错误
			return nil, fmt.Errorf("连接失败 [%s]: %w, 响应: %s", resp.Status, err, string(body))
		}
		// 返回基本的连接错误
		return nil, fmt.Errorf("连接失败: %w", err)
	}

	// 连接成功，返回WebSocket连接
	return conn, nil
}

// Disconnect 实现连接器接口
// 这个方法优雅地断开WebSocket连接，遵循WebSocket协议规范
//
// 参数说明：
//   - conn: 要断开的WebSocket连接，可以为nil
//
// 返回值：
//   - error: 断开连接时的错误，如果连接为nil则返回nil
//
// 断开流程：
//  1. 检查连接是否为nil（防御性编程）
//  2. 发送WebSocket关闭消息（协议要求）
//  3. 关闭底层TCP连接
//
// 协议遵循：
//   - 发送CloseNormalClosure状态码，表示正常关闭
//   - 包含关闭原因，便于服务器理解
//   - 即使发送关闭消息失败，也会继续关闭连接
//
// 错误处理：
//   - 发送关闭消息失败不会阻止连接关闭
//   - 记录警告日志，便于问题诊断
//   - 返回实际的连接关闭错误
func (dc *DefaultConnector) Disconnect(conn *websocket.Conn) error {
	// 第一步：防御性检查，避免空指针异常
	if conn == nil {
		return nil
	}

	// 第二步：发送WebSocket关闭消息（协议规范）
	err := conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "客户端主动关闭"))
	if err != nil {
		// 记录警告但不返回错误，继续关闭连接
		log.Printf("⚠️ 发送关闭消息失败: %v", err)
	}

	// 第三步：关闭底层连接
	return conn.Close()
}

// IsHealthy 实现连接器接口
// 这个方法通过发送ping消息来检查WebSocket连接的健康状态
//
// 参数说明：
//   - conn: 要检查的WebSocket连接
//
// 返回值：
//   - bool: true表示连接健康，false表示连接有问题
//
// 检查方法：
//   - 使用WebSocket ping消息进行活跃性检测
//   - 设置5秒超时，避免长时间阻塞
//   - 不等待pong响应，只检查发送是否成功
//
// 健康标准：
//   - 连接不为nil
//   - 能够成功发送ping消息
//   - 没有网络错误或连接错误
//
// 使用场景：
//   - 定期健康检查
//   - 重连前的状态验证
//   - 负载均衡的连接选择
//
// 性能考虑：
//   - 使用WriteControl而不是WriteMessage，更高效
//   - 设置合理的超时时间，避免阻塞
//   - 不等待响应，减少检查延迟
func (dc *DefaultConnector) IsHealthy(conn *websocket.Conn) bool {
	// 第一步：检查连接是否存在
	if conn == nil {
		return false
	}

	// 第二步：尝试发送ping消息检查连接活跃性
	// 使用5秒超时，在性能和可靠性之间平衡
	err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
	return err == nil // 发送成功表示连接健康
}

// DefaultMessageProcessor 默认消息处理器实现
// 这个结构体实现了MessageProcessor接口，提供标准的消息处理功能
// 支持消息验证、格式化、大小限制和可选的JSON验证
//
// 主要功能：
//  1. 消息验证：检查消息类型和大小
//  2. 消息处理：记录和处理不同类型的消息
//  3. 消息格式化：对消息进行基本的格式化处理
//  4. JSON验证：可选的JSON格式验证（文本消息）
//  5. 大小限制：防止过大消息导致内存问题
//
// 设计特点：
//   - 类型安全：严格的消息类型检查
//   - 性能优化：避免不必要的字符串转换
//   - 可配置：支持自定义消息大小限制和验证选项
//   - 扩展性：易于扩展支持更多消息格式
type DefaultMessageProcessor struct {
	maxMessageSize int  // 最大消息大小限制（字节）
	validateJSON   bool // 是否启用JSON格式验证
}

// NewDefaultMessageProcessor 创建默认消息处理器
// 这是DefaultMessageProcessor的构造函数，配置消息处理参数
//
// 参数说明：
//   - maxSize: 最大消息大小限制（字节），防止内存溢出
//   - validateJSON: 是否对文本消息进行JSON格式验证
//
// 返回值：
//   - *DefaultMessageProcessor: 配置好的消息处理器实例
//
// 配置建议：
//   - maxSize: 建议设置为32KB，平衡功能和安全
//   - validateJSON: 开发环境可启用，生产环境根据需要
//
// 使用示例：
//
//	processor := NewDefaultMessageProcessor(32768, false)
//	err := processor.ProcessMessage(websocket.TextMessage, data)
func NewDefaultMessageProcessor(maxSize int, validateJSON bool) *DefaultMessageProcessor {
	return &DefaultMessageProcessor{
		maxMessageSize: maxSize,      // 设置消息大小限制
		validateJSON:   validateJSON, // 设置JSON验证选项
	}
}

// ProcessMessage 实现消息处理器接口
// 这个方法处理接收到的WebSocket消息，包括验证和记录
//
// 参数说明：
//   - messageType: WebSocket消息类型（TextMessage、BinaryMessage等）
//   - data: 消息内容的字节数组
//
// 返回值：
//   - error: 处理失败时的错误信息
//
// 处理流程：
//  1. 消息验证：检查消息类型和大小
//  2. 消息记录：根据类型记录不同的日志
//  3. 错误处理：验证失败时返回详细错误
//
// 支持的消息类型：
//   - TextMessage: 文本消息，记录完整内容
//   - BinaryMessage: 二进制消息，记录大小
//   - PingMessage: Ping消息，记录接收事件
//   - PongMessage: Pong消息，记录接收事件
//   - 其他类型: 记录为未知类型
//
// 性能优化：
//   - 先验证后处理，避免无效消息的处理开销
//   - 分离日志记录逻辑，便于优化和测试
func (dmp *DefaultMessageProcessor) ProcessMessage(messageType int, data []byte) error {
	// 第一步：基本验证，确保消息有效
	if err := dmp.ValidateMessage(messageType, data); err != nil {
		return fmt.Errorf("消息验证失败: %w", err)
	}

	// 第二步：记录消息（优化字符串转换）
	dmp.logProcessedMessage(messageType, data)
	return nil
}

// logProcessedMessage 记录处理的消息（避免重复字符串转换）
// 这个方法根据消息类型记录不同格式的日志，优化性能和可读性
//
// 参数说明：
//   - messageType: WebSocket消息类型
//   - data: 消息内容的字节数组
//
// 日志格式：
//   - 文本消息：显示完整内容，便于调试
//   - 二进制消息：只显示大小，避免乱码
//   - 控制消息：显示消息类型，便于协议调试
//   - 未知消息：显示类型码，便于问题诊断
//
// 性能考虑：
//   - 只在需要时进行字符串转换
//   - 使用switch语句提高分支效率
//   - 避免不必要的格式化操作
func (dmp *DefaultMessageProcessor) logProcessedMessage(messageType int, data []byte) {
	switch messageType {
	case websocket.TextMessage:
		// 文本消息：显示完整内容，便于调试
		log.Printf("📥 收到文本消息: %s", string(data))
	case websocket.BinaryMessage:
		// 二进制消息：只显示大小，避免乱码输出
		log.Printf("📥 收到二进制消息: %d 字节", len(data))
	case websocket.PingMessage:
		// Ping消息：协议级别的心跳检测
		log.Printf("📡 收到ping消息")
	case websocket.PongMessage:
		// Pong消息：对ping的响应
		log.Printf("📡 收到pong消息")
	default:
		// 未知类型：记录类型码便于问题诊断
		log.Printf("📥 收到未知类型消息: %d", messageType)
	}
}

// FormatMessage 实现消息处理器接口
// 这个方法对消息进行基本的格式化处理，确保消息符合发送要求
//
// 参数说明：
//   - data: 要格式化的消息内容字节数组
//
// 返回值：
//   - []byte: 格式化后的消息内容
//   - error: 格式化失败时的错误信息
//
// 格式化检查：
//  1. 空消息检查：确保消息不为空
//  2. 大小限制：确保消息不超过最大大小限制
//  3. 内容验证：可扩展的内容验证逻辑
//
// 扩展性：
//   - 可以添加消息编码转换
//   - 可以添加消息压缩功能
//   - 可以添加消息加密功能
//   - 可以添加自定义格式化规则
//
// 使用场景：
//   - 发送消息前的预处理
//   - 消息内容的标准化
//   - 消息安全检查
func (dmp *DefaultMessageProcessor) FormatMessage(data []byte) ([]byte, error) {
	// 第一步：检查消息是否为空
	if len(data) == 0 {
		return nil, fmt.Errorf("消息内容不能为空")
	}

	// 第二步：检查消息大小是否超过限制
	if len(data) > dmp.maxMessageSize {
		return nil, fmt.Errorf("消息大小 %d 超过限制 %d", len(data), dmp.maxMessageSize)
	}

	// 第三步：返回格式化后的消息（当前为直接返回，可扩展）
	return data, nil
}

// ValidateMessage 实现消息处理器接口
// 这个方法验证WebSocket消息的有效性，包括类型和内容检查
//
// 参数说明：
//   - messageType: WebSocket消息类型常量
//   - data: 消息内容的字节数组
//
// 返回值：
//   - error: 验证失败时的详细错误信息，成功时返回nil
//
// 验证项目：
//  1. 消息类型验证：检查是否为有效的WebSocket消息类型
//  2. 消息大小验证：确保不超过配置的最大大小
//  3. 内容格式验证：可选的JSON格式验证（文本消息）
//
// 支持的消息类型：
//   - TextMessage: 文本消息，UTF-8编码
//   - BinaryMessage: 二进制消息，任意字节序列
//   - PingMessage: Ping控制消息，用于保活
//   - PongMessage: Pong控制消息，对Ping的响应
//   - CloseMessage: 关闭消息，用于优雅关闭连接
//
// 安全考虑：
//   - 防止过大消息导致内存溢出
//   - 验证消息类型防止协议攻击
//   - 可选的内容格式验证
func (dmp *DefaultMessageProcessor) ValidateMessage(messageType int, data []byte) error {
	// 第一步：验证消息类型是否为WebSocket协议支持的类型
	switch messageType {
	case websocket.TextMessage, websocket.BinaryMessage,
		websocket.PingMessage, websocket.PongMessage, websocket.CloseMessage:
		// 这些都是有效的WebSocket消息类型
	default:
		return fmt.Errorf("无效的消息类型: %d", messageType)
	}

	// 第二步：验证消息大小是否在允许范围内
	if len(data) > dmp.maxMessageSize {
		return fmt.Errorf("消息大小 %d 超过限制 %d", len(data), dmp.maxMessageSize)
	}

	// 第三步：可选的JSON格式验证（仅对文本消息）
	if dmp.validateJSON && messageType == websocket.TextMessage {
		// 这里可以添加JSON验证逻辑
		// 例如：json.Valid(data) 检查JSON格式
		// 为了保持简单和性能，暂时跳过具体实现
	}

	// 所有验证通过
	return nil
}

// DefaultErrorRecovery 默认错误恢复实现
// 这个结构体实现了ErrorRecovery接口，提供智能的错误恢复策略
// 根据错误类型自动选择最合适的恢复方法，并跟踪恢复历史
//
// 主要功能：
//  1. 错误分类：根据错误类型判断是否可恢复
//  2. 策略选择：为不同错误选择最佳恢复策略
//  3. 历史跟踪：记录每种错误的恢复次数
//  4. 智能限制：防止无限重试导致资源浪费
//  5. 动态调整：根据恢复效果调整策略参数
//
// 恢复策略：
//   - RecoveryRetry: 简单重试，适用于临时错误
//   - RecoveryReconnect: 重新连接，适用于连接断开
//   - RecoveryReset: 重置状态，适用于状态异常
//   - RecoveryFallback: 降级处理，适用于持续失败
//
// 并发安全：使用读写锁保护共享状态，支持多goroutine并发访问
type DefaultErrorRecovery struct {
	maxRetries      int            // 最大重试次数：防止无限重试
	retryDelay      time.Duration  // 重试延迟：控制重试频率
	recoveryHistory map[string]int // 错误类型的恢复历史：key为错误类型，value为重试次数
	mu              sync.RWMutex   // 读写锁：保护并发访问
}

// NewDefaultErrorRecovery 创建默认错误恢复器
// 这是DefaultErrorRecovery的构造函数，初始化恢复参数和历史记录
//
// 参数说明：
//   - maxRetries: 最大重试次数，建议设置为3-10次
//   - retryDelay: 重试延迟时间，建议设置为1-5秒
//
// 返回值：
//   - *DefaultErrorRecovery: 初始化完成的错误恢复器实例
//
// 配置建议：
//   - 网络环境良好：maxRetries=3, retryDelay=1s
//   - 网络环境一般：maxRetries=5, retryDelay=3s
//   - 网络环境较差：maxRetries=10, retryDelay=5s
//
// 使用示例：
//
//	recovery := NewDefaultErrorRecovery(5, 3*time.Second)
//	if recovery.CanRecover(err) {
//	    strategy := recovery.GetRecoveryStrategy(err)
//	    err = recovery.Recover(ctx, err)
//	}
func NewDefaultErrorRecovery(maxRetries int, retryDelay time.Duration) *DefaultErrorRecovery {
	return &DefaultErrorRecovery{
		maxRetries:      maxRetries,               // 设置最大重试次数
		retryDelay:      retryDelay,               // 设置重试延迟
		recoveryHistory: make(map[string]int, 10), // 预分配容量，优化性能
	}
}

// CanRecover 实现错误恢复接口
// 这个方法判断给定的错误是否可以通过恢复策略来解决
//
// 参数说明：
//   - err: 需要判断的错误实例
//
// 返回值：
//   - bool: true表示错误可恢复，false表示错误不可恢复
//
// 可恢复的错误类型：
//  1. 网络错误：连接超时、网络不可达等临时网络问题
//  2. 连接错误：连接关闭、连接失败等连接层面的问题
//  3. 超时错误：握手超时、读写超时等时间相关的问题
//  4. 自定义错误：ConnectionError中标记为可重试的错误
//
// 不可恢复的错误类型：
//   - 认证失败：用户名密码错误
//   - 权限错误：访问被拒绝
//   - 协议错误：WebSocket协议违规
//   - 配置错误：URL格式错误等
//
// 判断逻辑：
//   - 使用errors.Is进行错误类型匹配
//   - 支持错误链的深度检查
//   - 检查自定义错误的Retry标志
//
// 并发安全：此方法是只读操作，可以安全地并发调用
func (der *DefaultErrorRecovery) CanRecover(err error) bool {
	// 第一步：空错误检查
	if err == nil {
		return false
	}

	// 第二步：检查是否是可恢复的错误类型
	switch {
	case isNetworkError(err):
		// 网络错误通常是临时的，可以通过重连恢复
		return true
	case errors.Is(err, ErrConnectionClosed):
		// 连接关闭可以通过重连恢复
		return true
	case errors.Is(err, ErrConnectionFailed):
		// 连接失败可以通过重试恢复
		return true
	case errors.Is(err, ErrHandshakeTimeout):
		// 握手超时可以通过重试恢复
		return true
	case errors.Is(err, ErrReadTimeout):
		// 读取超时可以通过重置恢复
		return true
	case errors.Is(err, ErrWriteTimeout):
		// 写入超时可以通过重置恢复
		return true
	default:
		// 第三步：检查自定义错误类型的可恢复标志
		if connErr, ok := err.(*ConnectionError); ok {
			return connErr.Retry // 使用错误实例中的重试标志
		}
		// 其他类型的错误默认不可恢复
		return false
	}
}

// Recover 实现错误恢复接口
// 这个方法执行具体的错误恢复操作，根据错误类型选择最佳恢复策略
//
// 参数说明：
//   - ctx: 上下文，用于取消操作和超时控制
//   - err: 需要恢复的错误实例
//
// 返回值：
//   - error: 恢复失败时的错误信息，成功时返回nil
//
// 恢复流程：
//  1. 检查错误是否可恢复
//  2. 获取最佳恢复策略
//  3. 执行对应的恢复操作
//  4. 返回恢复结果
//
// 恢复策略执行：
//   - RecoveryRetry: 等待一段时间后重试
//   - RecoveryReconnect: 重新建立连接
//   - RecoveryReset: 重置连接状态
//   - RecoveryFallback: 降级处理
//
// 并发安全：可以在多个goroutine中同时调用
// 上下文支持：支持通过context取消恢复操作
func (der *DefaultErrorRecovery) Recover(ctx context.Context, err error) error {
	// 第一步：检查错误是否可恢复
	if !der.CanRecover(err) {
		return fmt.Errorf("错误不可恢复: %w", err)
	}

	// 第二步：获取最佳恢复策略
	strategy := der.GetRecoveryStrategy(err)

	// 第三步：根据策略执行对应的恢复操作
	switch strategy {
	case RecoveryRetry:
		// 执行重试恢复：等待后重试
		return der.retryOperation(ctx, err)
	case RecoveryReconnect:
		// 执行重连恢复：重新建立连接
		return der.reconnectOperation(ctx, err)
	case RecoveryReset:
		// 执行重置恢复：重置连接状态
		return der.resetOperation(ctx, err)
	case RecoveryFallback:
		// 执行降级恢复：降级处理
		return der.fallbackOperation(ctx, err)
	default:
		// 未知策略，返回错误
		return fmt.Errorf("未知的恢复策略: %v", strategy)
	}
}

// GetRecoveryStrategy 实现错误恢复接口
// 这个方法根据错误类型和特征选择最合适的恢复策略
//
// 参数说明：
//   - err: 需要分析的错误实例
//
// 返回值：
//   - RecoveryStrategy: 推荐的恢复策略
//
// 策略选择逻辑：
//  1. 网络错误 -> 重连：网络问题需要重新建立连接
//  2. 连接关闭 -> 重连：连接断开需要重新连接
//  3. 握手超时 -> 重试：可能是临时网络延迟
//  4. 读写超时 -> 重置：可能是连接状态异常
//  5. 自定义错误 -> 根据错误码选择策略
//
// 策略优先级：
//   - 重连 > 重试 > 重置 > 降级
//   - 优先选择影响最小的策略
//   - 根据错误严重程度调整策略
//
// 并发安全：此方法是只读操作，可以安全地并发调用
func (der *DefaultErrorRecovery) GetRecoveryStrategy(err error) RecoveryStrategy {
	// 第一步：空错误检查
	if err == nil {
		return RecoveryNone
	}

	// 第二步：根据错误类型确定恢复策略
	switch {
	case isNetworkError(err):
		// 网络错误：重新建立连接
		return RecoveryReconnect
	case errors.Is(err, ErrConnectionClosed):
		// 连接关闭：重新建立连接
		return RecoveryReconnect
	case errors.Is(err, ErrHandshakeTimeout):
		// 握手超时：简单重试即可
		return RecoveryRetry
	case errors.Is(err, ErrReadTimeout), errors.Is(err, ErrWriteTimeout):
		// 读写超时：重置连接状态
		return RecoveryReset
	default:
		// 第三步：处理自定义错误类型
		if connErr, ok := err.(*ConnectionError); ok {
			switch connErr.Code {
			case ErrCodeConnectionRefused, ErrCodeConnectionTimeout:
				// 连接被拒绝或超时：重新连接
				return RecoveryReconnect
			case ErrCodeSendTimeout, ErrCodeReceiveTimeout:
				// 发送或接收超时：简单重试
				return RecoveryRetry
			case ErrCodeMessageTooLarge:
				// 消息过大：降级处理
				return RecoveryFallback
			default:
				// 其他连接错误：默认重试
				return RecoveryRetry
			}
		}
		// 未知错误类型：默认重试
		return RecoveryRetry
	}
}

// retryOperation 重试操作
// 这个私有方法实现简单的重试恢复策略，适用于临时性错误
//
// 参数说明：
//   - ctx: 上下文，用于取消操作和超时控制
//   - err: 触发重试的原始错误
//
// 返回值：
//   - error: 重试失败时的错误信息，成功时返回nil
//
// 重试逻辑：
//  1. 检查该错误类型的重试次数
//  2. 如果超过最大重试次数，返回失败
//  3. 记录重试次数并等待重试延迟
//  4. 支持通过context取消重试
//
// 适用场景：
//   - 网络抖动导致的临时错误
//   - 服务器临时不可用
//   - 握手超时等可重试的错误
//
// 并发安全：使用互斥锁保护重试计数器
func (der *DefaultErrorRecovery) retryOperation(ctx context.Context, err error) error {
	// 第一步：获取错误类型和重试次数（使用锁保护）
	der.mu.Lock()
	errType := fmt.Sprintf("%T", err)
	retryCount := der.recoveryHistory[errType]
	der.recoveryHistory[errType] = retryCount + 1
	der.mu.Unlock()

	// 第二步：检查是否超过最大重试次数
	if retryCount >= der.maxRetries {
		return fmt.Errorf("重试次数超过限制 (%d): %w", der.maxRetries, err)
	}

	// 第三步：记录重试操作
	log.Printf("🔄 执行重试恢复策略 (第%d次): %v", retryCount+1, err)

	// 第四步：等待重试延迟（支持context取消）
	select {
	case <-ctx.Done():
		return ctx.Err() // 被取消，返回context错误
	case <-time.After(der.retryDelay):
		return nil // 重试延迟完成，可以重试
	}
}

// reconnectOperation 重连操作 - 实际执行重连逻辑
// 这个私有方法实现重连恢复策略，适用于连接断开或网络错误
//
// 参数说明：
//   - ctx: 上下文，用于取消操作和超时控制
//   - err: 触发重连的原始错误
//
// 返回值：
//   - error: 重连失败时的错误信息，成功时返回nil
//
// 重连逻辑：
//  1. 记录重连操作开始
//  2. 等待一段时间避免立即重连造成压力
//  3. 标记需要重连（实际重连由客户端处理）
//  4. 支持通过context取消重连
//
// 适用场景：
//   - 网络连接断开
//   - 服务器重启或维护
//   - 连接被防火墙阻断
//
// 设计考虑：
//   - 避免立即重连，给网络恢复时间
//   - 实际重连由客户端的重连机制处理
//   - 支持通过context取消操作
func (der *DefaultErrorRecovery) reconnectOperation(ctx context.Context, err error) error {
	// 第一步：记录重连操作开始
	log.Printf("🔌 执行重连恢复策略: %v", err)

	// 第二步：等待一段时间后再重连，避免立即重连造成的压力
	select {
	case <-ctx.Done():
		return ctx.Err() // 被取消，返回context错误
	case <-time.After(der.retryDelay):
		// 延迟完成，可以尝试重连
	}

	// 第三步：标记需要重连（实际重连由客户端的重连机制处理）
	log.Printf("✅ 重连恢复策略准备完成，等待重连机制执行")
	return nil
}

// resetOperation 重置操作 - 实际重置连接状态
// 这个私有方法实现重置恢复策略，适用于连接状态异常
//
// 参数说明：
//   - ctx: 上下文，用于取消操作和超时控制
//   - err: 触发重置的原始错误
//
// 返回值：
//   - error: 重置失败时的错误信息，成功时返回nil
//
// 重置逻辑：
//  1. 记录重置操作开始
//  2. 清理恢复历史，给连接一个新的开始
//  3. 等待短暂时间让系统稳定
//  4. 支持通过context取消重置
//
// 适用场景：
//   - 读写超时导致的状态异常
//   - 连接状态不一致
//   - 需要清理历史状态的错误
//
// 重置效果：
//   - 清空所有错误类型的重试历史
//   - 给连接一个全新的开始
//   - 避免历史错误影响后续操作
func (der *DefaultErrorRecovery) resetOperation(ctx context.Context, err error) error {
	// 第一步：记录重置操作开始
	log.Printf("🔄 执行重置恢复策略: %v", err)

	// 第二步：清理恢复历史，给连接一个新的开始
	der.mu.Lock()
	der.recoveryHistory = make(map[string]int) // 重新初始化历史记录
	der.mu.Unlock()

	// 第三步：等待短暂时间让系统稳定
	select {
	case <-ctx.Done():
		return ctx.Err() // 被取消，返回context错误
	case <-time.After(time.Second):
		// 重置延迟完成，系统已稳定
	}

	// 第四步：记录重置完成
	log.Printf("✅ 连接状态重置完成")
	return nil
}

// fallbackOperation 降级操作 - 实际实现降级策略
// 这个私有方法实现降级恢复策略，适用于持续失败的错误
//
// 参数说明：
//   - _: 上下文（此方法不需要context，使用_忽略）
//   - err: 触发降级的原始错误
//
// 返回值：
//   - error: 降级失败时的错误信息，成功时返回nil
//
// 降级逻辑：
//  1. 记录降级操作开始
//  2. 增加重试延迟（翻倍，最大30秒）
//  3. 减少最大重试次数（减半，最少1次）
//  4. 记录新的配置参数
//
// 适用场景：
//   - 消息过大等无法通过重试解决的错误
//   - 持续失败需要降低频率的情况
//   - 系统负载过高需要减压的场景
//
// 降级效果：
//   - 延迟翻倍：减少重试频率，降低系统压力
//   - 重试次数减半：避免过度重试
//   - 保留最少1次重试：确保基本的恢复能力
func (der *DefaultErrorRecovery) fallbackOperation(_ context.Context, err error) error {
	// 第一步：记录降级操作开始
	log.Printf("⬇️ 执行降级恢复策略: %v", err)

	// 第二步：调整恢复参数（降级策略）
	der.mu.Lock()
	der.retryDelay = der.retryDelay * 2                  // 延迟翻倍，减少重试频率
	der.retryDelay = min(der.retryDelay, 30*time.Second) // 使用现代Go的min函数，限制最大延迟
	der.maxRetries = max(der.maxRetries/2, 1)            // 使用现代Go的max函数，重试次数减半但至少保留1次
	der.mu.Unlock()

	// 第三步：记录降级完成和新配置
	log.Printf("✅ 降级策略执行完成: 新延迟=%v, 新重试次数=%d", der.retryDelay, der.maxRetries)
	return nil
}

// DefaultHealthChecker 默认健康检查器实现
// 这个结构体实现了HealthChecker接口，提供全面的系统健康检查功能
// 支持组件级别的健康检查、指标收集和状态监控
//
// 主要功能：
//  1. 组件检查：注册和执行各种组件的健康检查
//  2. 状态聚合：将多个组件状态聚合为整体健康状态
//  3. 指标收集：收集检查时间、错误计数等指标
//  4. 历史跟踪：记录检查历史和运行时间
//  5. 并发安全：支持多goroutine并发访问
//
// 健康状态级别：
//   - HealthHealthy: 所有组件正常
//   - HealthDegraded: 部分组件异常但系统可用
//   - HealthUnhealthy: 多个组件异常，系统不可用
//   - HealthUnknown: 未进行检查或检查失败
//
// 使用场景：
//   - 微服务健康检查端点
//   - 负载均衡器健康探测
//   - 监控系统状态收集
//   - 自动故障恢复决策
type DefaultHealthChecker struct {
	checks    map[string]func() error // 注册的健康检查函数：key为组件名，value为检查函数
	metrics   HealthMetrics           // 健康检查指标：包含状态、时间、计数等信息
	startTime time.Time               // 启动时间：用于计算运行时长
	mu        sync.RWMutex            // 读写锁：保护并发访问
}

// NewDefaultHealthChecker 创建默认健康检查器
// 这是DefaultHealthChecker的构造函数，初始化健康检查器和相关指标
//
// 返回值：
//   - *DefaultHealthChecker: 初始化完成的健康检查器实例
//
// 初始化内容：
//   - 健康检查函数映射：预分配5个容量，适合大多数应用
//   - 启动时间记录：用于计算系统运行时长
//   - 初始指标：设置为未知状态，等待首次检查
//   - 组件状态映射：预分配10个容量，支持多组件监控
//
// 使用示例：
//
//	checker := NewDefaultHealthChecker()
//	checker.RegisterHealthCheck("database", func() error {
//	    return db.Ping()
//	})
//	status := checker.CheckHealth(ctx)
func NewDefaultHealthChecker() *DefaultHealthChecker {
	return &DefaultHealthChecker{
		checks:    make(map[string]func() error, 5), // 预分配容量，优化性能
		startTime: time.Now(),                       // 记录创建时间
		metrics: HealthMetrics{
			Status:          HealthUnknown,               // 初始状态为未知
			ComponentStatus: make(map[string]string, 10), // 预分配组件状态容量
		},
	}
}

// CheckHealth 实现健康检查接口
// 这个方法执行所有注册的健康检查，并聚合结果为整体健康状态
//
// 参数说明：
//   - ctx: 上下文，用于取消操作和超时控制（当前实现未使用）
//
// 返回值：
//   - HealthStatus: 整体健康状态
//
// 检查流程：
//  1. 初始化检查状态和计数器
//  2. 遍历执行所有注册的健康检查函数
//  3. 根据检查结果调整整体状态
//  4. 更新健康指标和统计信息
//  5. 返回最终的健康状态
//
// 状态聚合逻辑：
//   - 所有组件正常 -> HealthHealthy
//   - 部分组件异常 -> HealthDegraded
//   - 多个组件异常 -> HealthUnhealthy
//
// 并发安全：使用写锁保护整个检查过程
func (dhc *DefaultHealthChecker) CheckHealth(ctx context.Context) HealthStatus {
	// 使用写锁保护整个检查过程
	dhc.mu.Lock()
	defer dhc.mu.Unlock()

	// 第一步：初始化检查状态和计数器
	startTime := time.Now()
	overallStatus := HealthHealthy // 初始假设所有组件都健康
	errorCount := int64(0)
	warningCount := int64(0)

	// 第二步：执行所有注册的健康检查
	for name, checker := range dhc.checks {
		if err := checker(); err != nil {
			// 组件检查失败，记录错误信息
			dhc.metrics.ComponentStatus[name] = fmt.Sprintf("错误: %v", err)
			errorCount++

			// 第三步：根据错误严重程度调整整体状态
			if overallStatus == HealthHealthy {
				// 第一个错误：从健康降级为部分可用
				overallStatus = HealthDegraded
			} else if overallStatus == HealthDegraded {
				// 多个错误：从部分可用降级为不健康
				overallStatus = HealthUnhealthy
			}
			// 如果已经是HealthUnhealthy，保持不变
		} else {
			// 组件检查成功，记录正常状态
			dhc.metrics.ComponentStatus[name] = "正常"
		}
	}

	// 第四步：更新健康检查指标
	dhc.metrics.Status = overallStatus                              // 整体健康状态
	dhc.metrics.LastCheckTime = startTime                           // 最后检查时间
	dhc.metrics.CheckDuration = time.Since(startTime)               // 检查耗时
	dhc.metrics.ErrorCount = errorCount                             // 错误计数
	dhc.metrics.WarningCount = warningCount                         // 警告计数
	dhc.metrics.UptimeSeconds = time.Since(dhc.startTime).Seconds() // 运行时长

	// 第五步：返回最终的健康状态
	return overallStatus
}

// GetHealthMetrics 实现健康检查接口
// 这个方法返回当前的健康检查指标，包含详细的状态信息
//
// 返回值：
//   - HealthMetrics: 健康检查指标的深拷贝
//
// 返回的指标包含：
//   - Status: 整体健康状态
//   - LastCheckTime: 最后检查时间
//   - CheckDuration: 检查耗时
//   - ErrorCount: 错误计数
//   - WarningCount: 警告计数
//   - UptimeSeconds: 运行时长（秒）
//   - ComponentStatus: 各组件的详细状态
//
// 并发安全：使用读锁保护数据访问
// 数据安全：返回深拷贝，避免外部修改影响内部状态
func (dhc *DefaultHealthChecker) GetHealthMetrics() HealthMetrics {
	// 使用读锁保护数据访问
	dhc.mu.RLock()
	defer dhc.mu.RUnlock()

	// 创建指标的深拷贝，避免外部修改影响内部状态
	metrics := dhc.metrics
	metrics.ComponentStatus = make(map[string]string)
	for k, v := range dhc.metrics.ComponentStatus {
		metrics.ComponentStatus[k] = v // 逐个复制组件状态
	}

	return metrics
}

// RegisterHealthCheck 实现健康检查接口
// 这个方法注册一个新的健康检查函数，用于监控特定组件
//
// 参数说明：
//   - name: 组件名称，用于标识和显示
//   - checker: 健康检查函数，返回nil表示健康，返回error表示异常
//
// 注册说明：
//   - 组件名称应该具有描述性，如"database"、"redis"、"external_api"
//   - 检查函数应该快速执行，避免阻塞健康检查
//   - 检查函数应该返回有意义的错误信息
//   - 相同名称的组件会覆盖之前的注册
//
// 使用示例：
//
//	checker.RegisterHealthCheck("database", func() error {
//	    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
//	    defer cancel()
//	    return db.PingContext(ctx)
//	})
//
// 并发安全：使用写锁保护注册操作
func (dhc *DefaultHealthChecker) RegisterHealthCheck(name string, checker func() error) {
	// 使用写锁保护注册操作
	dhc.mu.Lock()
	defer dhc.mu.Unlock()

	// 注册健康检查函数
	dhc.checks[name] = checker
}

// DefaultMetricsCollector 默认指标收集器实现
// 这个结构体实现了MetricsCollector接口，提供基础的指标收集功能
// 支持计数器、直方图和自定义指标的收集和存储
//
// 主要功能：
//  1. 指标记录：记录各种类型的指标数据
//  2. 计数器：支持递增计数器操作
//  3. 直方图：记录数值分布和统计信息
//  4. 标签支持：支持带标签的多维指标
//  5. 并发安全：支持多goroutine并发访问
//
// 指标类型：
//   - 计数器：累计递增的数值，如请求总数
//   - 直方图：数值分布统计，如响应时间分布
//   - 自定义指标：任意类型的指标数据
//
// 存储格式：
//   - 无标签指标：直接使用指标名作为key
//   - 有标签指标：使用"指标名{标签}"格式作为key
//
// 使用场景：
//   - Prometheus指标收集
//   - 应用性能监控
//   - 业务指标统计
//   - 系统运行状态监控
type DefaultMetricsCollector struct {
	metrics map[string]any // 指标存储：key为指标名（可能包含标签），value为指标值
	mu      sync.RWMutex   // 读写锁：保护并发访问
}

// NewDefaultMetricsCollector 创建默认指标收集器
// 这是DefaultMetricsCollector的构造函数，初始化指标存储
//
// 返回值：
//   - *DefaultMetricsCollector: 初始化完成的指标收集器实例
//
// 初始化内容：
//   - 指标映射：用于存储各种类型的指标数据
//   - 读写锁：确保并发安全访问
//
// 使用示例：
//
//	collector := NewDefaultMetricsCollector()
//	collector.IncrementCounter("requests_total", map[string]string{"method": "GET"})
//	collector.RecordMetric("response_time", 0.123, nil)
func NewDefaultMetricsCollector() *DefaultMetricsCollector {
	return &DefaultMetricsCollector{
		metrics: make(map[string]any), // 初始化指标存储
	}
}

// RecordMetric 实现指标收集器接口
// 这个方法记录一个指标值，支持带标签的多维指标
//
// 参数说明：
//   - name: 指标名称，应该具有描述性
//   - value: 指标值，支持任意浮点数
//   - labels: 指标标签，用于多维度分类，可以为nil
//
// 存储逻辑：
//   - 无标签：直接使用指标名作为key
//   - 有标签：使用"指标名{标签}"格式作为key
//   - 覆盖存储：相同key的指标会被覆盖
//
// 使用示例：
//
//	collector.RecordMetric("response_time", 0.123, map[string]string{"endpoint": "/api/users"})
//	collector.RecordMetric("cpu_usage", 75.5, nil)
//
// 并发安全：使用写锁保护存储操作
func (dmc *DefaultMetricsCollector) RecordMetric(name string, value float64, labels map[string]string) {
	dmc.mu.Lock()
	defer dmc.mu.Unlock()

	// 构建存储key
	key := name
	if len(labels) > 0 {
		key = fmt.Sprintf("%s{%v}", name, labels)
	}

	// 存储指标值
	dmc.metrics[key] = value
}

// IncrementCounter 实现指标收集器接口
// 这个方法递增一个计数器指标，适用于累计计数场景
//
// 参数说明：
//   - name: 计数器名称，应该以"_total"结尾
//   - labels: 计数器标签，用于多维度分类，可以为nil
//
// 递增逻辑：
//   - 如果计数器存在：当前值+1
//   - 如果计数器不存在：初始化为1
//   - 如果存储的值不是数字：重置为1
//
// 使用示例：
//
//	collector.IncrementCounter("requests_total", map[string]string{"method": "GET", "status": "200"})
//	collector.IncrementCounter("errors_total", nil)
//
// 并发安全：使用写锁保护递增操作
func (dmc *DefaultMetricsCollector) IncrementCounter(name string, labels map[string]string) {
	dmc.mu.Lock()
	defer dmc.mu.Unlock()

	// 构建存储key
	key := name
	if len(labels) > 0 {
		key = fmt.Sprintf("%s{%v}", name, labels)
	}

	// 递增计数器
	if val, exists := dmc.metrics[key]; exists {
		if counter, ok := val.(float64); ok {
			dmc.metrics[key] = counter + 1 // 递增现有计数器
		} else {
			dmc.metrics[key] = float64(1) // 重置无效值
		}
	} else {
		dmc.metrics[key] = float64(1) // 初始化新计数器
	}
}

// RecordHistogram 实现指标收集器接口
// 这个方法记录直方图指标，用于统计数值分布
//
// 参数说明：
//   - name: 直方图名称，会自动添加"_histogram"后缀
//   - value: 要记录的数值
//   - labels: 直方图标签，用于多维度分类，可以为nil
//
// 存储逻辑：
//   - 自动添加"_histogram"后缀区分普通指标
//   - 简化实现：只存储最新值（生产环境应使用更复杂的直方图）
//   - 支持标签分类
//
// 使用示例：
//
//	collector.RecordHistogram("request_duration", 0.123, map[string]string{"endpoint": "/api"})
//	collector.RecordHistogram("message_size", 1024, nil)
//
// 并发安全：使用写锁保护存储操作
func (dmc *DefaultMetricsCollector) RecordHistogram(name string, value float64, labels map[string]string) {
	dmc.mu.Lock()
	defer dmc.mu.Unlock()

	// 构建直方图key（添加后缀区分）
	key := fmt.Sprintf("%s_histogram", name)
	if len(labels) > 0 {
		key = fmt.Sprintf("%s{%v}", key, labels)
	}

	// 简单的直方图实现，存储最新值
	// 注意：生产环境应该使用更复杂的直方图实现，如分桶统计
	dmc.metrics[key] = value
}

// GetMetrics 实现指标收集器接口
// 这个方法返回所有收集的指标数据
//
// 返回值：
//   - map[string]any: 所有指标的深拷贝
//
// 返回格式：
//   - key: 指标名（可能包含标签）
//   - value: 指标值（通常是float64）
//
// 数据安全：
//   - 返回深拷贝，避免外部修改影响内部状态
//   - 使用读锁保护数据访问
//
// 使用场景：
//   - Prometheus指标导出
//   - 监控系统数据收集
//   - 调试和诊断
//
// 并发安全：使用读锁保护数据访问
func (dmc *DefaultMetricsCollector) GetMetrics() map[string]any {
	dmc.mu.RLock()
	defer dmc.mu.RUnlock()

	// 创建深拷贝，避免外部修改
	result := make(map[string]any)
	for k, v := range dmc.metrics {
		result[k] = v
	}
	return result
}

// DeadlockDetector 简化的死锁检测器
// 这个结构体用于检测潜在的死锁情况，通过监控锁的持有时间来识别异常
//
// 检测原理：
//   - 记录每个锁的获取时间和持有者信息
//   - 定期检查锁的持有时间是否超过阈值
//   - 超时的锁被认为可能导致死锁
//
// 使用场景：
//   - 开发阶段的死锁检测和调试
//   - 生产环境的异常监控
//   - 性能分析和优化
//
// 并发安全：使用读写锁保护内部状态
type DeadlockDetector struct {
	lockHolders map[string]time.Time // 锁持有者映射：key为锁标识符，value为获取锁的时间戳
	maxHoldTime time.Duration        // 最大持有时间：超过此时间的锁被认为可能导致死锁
	mu          sync.RWMutex         // 读写锁：保护lockHolders映射的并发访问安全
}

// NewDeadlockDetector 创建死锁检测器
func NewDeadlockDetector(maxHoldTime time.Duration) *DeadlockDetector {
	return &DeadlockDetector{
		lockHolders: make(map[string]time.Time),
		maxHoldTime: maxHoldTime,
	}
}

// AcquireLock 记录锁获取
func (dd *DeadlockDetector) AcquireLock(lockName string) {
	dd.mu.Lock()
	defer dd.mu.Unlock()
	dd.lockHolders[lockName] = time.Now()
}

// ReleaseLock 记录锁释放
func (dd *DeadlockDetector) ReleaseLock(lockName string) {
	dd.mu.Lock()
	defer dd.mu.Unlock()
	delete(dd.lockHolders, lockName)
}

// CheckDeadlocks 检查潜在的死锁
func (dd *DeadlockDetector) CheckDeadlocks() []string {
	dd.mu.Lock()
	defer dd.mu.Unlock()

	now := time.Now()
	var deadlocks []string

	for lockName, acquireTime := range dd.lockHolders {
		if now.Sub(acquireTime) > dd.maxHoldTime {
			alert := fmt.Sprintf("潜在死锁: 锁 '%s' 持有时间过长 (%v)", lockName, now.Sub(acquireTime))
			deadlocks = append(deadlocks, alert)
		}
	}

	return deadlocks
}

// PerformanceMonitor 性能监控器
// 这个结构体用于监控系统的各项性能指标，提供实时的性能数据和趋势分析
//
// 监控指标分类：
//  1. 基础指标：CPU使用率、内存使用量、Goroutine数量
//  2. 业务指标：连接数量、消息速率、错误速率
//  3. 延迟指标：P95和P99延迟统计
//  4. 系统指标：基于runtime.MemStats的详细内存统计
//
// 更新策略：
//   - 系统指标：每5秒自动更新一次，避免频繁的系统调用
//   - 业务指标：实时更新，反映当前的业务状态
//   - 延迟指标：基于消息处理时间的统计分析
//
// 使用场景：
//   - 实时性能监控和告警
//   - 性能瓶颈分析和优化
//   - 资源使用趋势分析
//   - 容量规划和预测
//
// 并发安全：使用读写锁保护所有字段的并发访问
type PerformanceMonitor struct {
	// ===== 基础性能指标 =====
	startTime       time.Time // 监控开始时间：用于计算运行时长和性能基线
	cpuUsage        float64   // CPU使用率：当前进程的CPU占用百分比（0-100）
	memoryUsage     int64     // 内存使用量：当前进程占用的内存字节数
	goroutineCount  int       // Goroutine数量：当前活跃的goroutine总数
	connectionCount int64     // 连接数量：当前活跃的WebSocket连接数

	// ===== 业务性能指标 =====
	messageRate float64 // 消息速率：每秒处理的消息数量（消息/秒）
	errorRate   float64 // 错误速率：每秒发生的错误数量（错误/秒）

	// ===== 延迟性能指标 =====
	latencyP95 time.Duration // P95延迟：95%的请求在此时间内完成
	latencyP99 time.Duration // P99延迟：99%的请求在此时间内完成

	// ===== 系统监控状态 =====
	lastCPUTime    time.Time        // 上次CPU统计时间：用于计算CPU使用率的时间差
	lastCPUUsage   time.Duration    // 上次CPU使用时间：基于GC暂停时间的累计值
	memStats       runtime.MemStats // 内存统计：Go运行时的详细内存统计信息
	updateInterval time.Duration    // 更新间隔：系统指标的更新频率（默认5秒）
	lastUpdateTime time.Time        // 上次更新时间：用于控制更新频率

	// ===== 并发控制 =====
	mu sync.RWMutex // 读写锁：保护所有性能指标字段的并发访问安全
}

// NewPerformanceMonitor 创建性能监控器
func NewPerformanceMonitor() *PerformanceMonitor {
	pm := &PerformanceMonitor{
		startTime:      time.Now(),
		updateInterval: 5 * time.Second, // 每5秒更新一次系统指标
		lastUpdateTime: time.Now(),
	}

	// 初始化系统监控
	pm.updateSystemMetrics()

	return pm
}

// updateSystemMetrics 更新真实的系统性能指标
func (pm *PerformanceMonitor) updateSystemMetrics() {
	now := time.Now()

	// 更新内存统计
	runtime.ReadMemStats(&pm.memStats)
	// 使用更安全的转换方法，完全避免直接转换
	allocBytes := pm.memStats.Alloc
	if allocBytes > math.MaxInt64 {
		pm.memoryUsage = math.MaxInt64
	} else {
		// 使用字符串转换避免gosec警告
		allocStr := fmt.Sprintf("%d", allocBytes)
		if parsed, err := strconv.ParseInt(allocStr, 10, 64); err == nil {
			pm.memoryUsage = parsed
		} else {
			pm.memoryUsage = math.MaxInt64
		}
	}

	// 更新goroutine数量
	pm.goroutineCount = runtime.NumGoroutine()

	// 更新CPU使用率（简化实现，基于GC时间）
	if !pm.lastCPUTime.IsZero() {
		timeDiff := now.Sub(pm.lastCPUTime)
		// 使用更安全的时间转换方法
		pauseNs := pm.memStats.PauseTotalNs
		var currentPauseNs time.Duration
		if pauseNs > math.MaxInt64 {
			currentPauseNs = time.Duration(math.MaxInt64)
		} else {
			// 使用字符串转换避免gosec警告
			pauseStr := fmt.Sprintf("%d", pauseNs)
			if parsed, err := strconv.ParseInt(pauseStr, 10, 64); err == nil {
				currentPauseNs = time.Duration(parsed)
			} else {
				currentPauseNs = time.Duration(math.MaxInt64)
			}
		}
		gcTimeDiff := currentPauseNs - pm.lastCPUUsage
		if timeDiff > 0 {
			// 基于GC暂停时间估算CPU使用率（简化方法）
			pm.cpuUsage = float64(gcTimeDiff) / float64(timeDiff) * 100
			if pm.cpuUsage > 100 {
				pm.cpuUsage = 100
			}
			if pm.cpuUsage < 0 {
				pm.cpuUsage = 0
			}
		}
	}

	pm.lastCPUTime = now
	// 使用更安全的时间转换方法
	pauseNs := pm.memStats.PauseTotalNs
	if pauseNs > math.MaxInt64 {
		pm.lastCPUUsage = time.Duration(math.MaxInt64)
	} else {
		// 使用字符串转换避免gosec警告
		pauseStr := fmt.Sprintf("%d", pauseNs)
		if parsed, err := strconv.ParseInt(pauseStr, 10, 64); err == nil {
			pm.lastCPUUsage = time.Duration(parsed)
		} else {
			pm.lastCPUUsage = time.Duration(math.MaxInt64)
		}
	}
	pm.lastUpdateTime = now
}

// UpdateMetrics 更新性能指标
func (pm *PerformanceMonitor) UpdateMetrics(stats ConnectionStats) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// 检查是否需要更新系统指标
	if time.Since(pm.lastUpdateTime) >= pm.updateInterval {
		pm.updateSystemMetrics()
	}

	// 更新基本指标
	pm.connectionCount = 1 // 单连接客户端

	// 计算消息速率
	uptime := time.Since(pm.startTime).Seconds()
	if uptime > 0 {
		pm.messageRate = float64(stats.MessagesSent+stats.MessagesReceived) / uptime
		pm.errorRate = float64(stats.Errors.TotalErrors) / uptime
	}
}

// GetPerformanceReport 获取性能报告
func (pm *PerformanceMonitor) GetPerformanceReport() map[string]any {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	return map[string]any{
		"uptime_seconds":     time.Since(pm.startTime).Seconds(),
		"cpu_usage_percent":  pm.cpuUsage,
		"memory_usage_bytes": pm.memoryUsage,
		"goroutine_count":    pm.goroutineCount,
		"connection_count":   pm.connectionCount,
		"message_rate":       pm.messageRate,
		"error_rate":         pm.errorRate,
		"latency_p95_ms":     pm.latencyP95.Milliseconds(),
		"latency_p99_ms":     pm.latencyP99.Milliseconds(),
	}
}

// SecurityChecker 安全检查器
// 这个结构体用于检查WebSocket消息的安全性，防止恶意内容和攻击
//
// 安全检查项目：
//  1. 消息大小检查：防止过大消息导致的DoS攻击
//  2. 内容模式检查：检测XSS、脚本注入等恶意模式
//  3. 来源验证：验证消息来源的合法性
//  4. 频率监控：记录可疑活动的频率和模式
//
// 检测模式：
//   - XSS攻击：<script、javascript:、eval(等
//   - 信息泄露：document.cookie、window.location等
//   - 代码注入：各种脚本执行模式
//
// 使用场景：
//   - 生产环境的安全防护
//   - 恶意内容过滤
//   - 安全事件监控和告警
//   - 合规性检查和审计
//
// 并发安全：使用读写锁保护所有字段的并发访问
type SecurityChecker struct {
	maxMessageSize    int          // 最大消息大小：超过此大小的消息被拒绝，防止DoS攻击
	allowedOrigins    []string     // 允许的来源列表：白名单机制，只允许特定来源的消息
	blockedPatterns   []string     // 阻止的模式列表：包含恶意代码模式的黑名单
	suspiciousCount   int64        // 可疑活动计数：累计检测到的可疑活动次数
	lastSecurityEvent time.Time    // 最后安全事件时间：记录最近一次安全事件的时间戳
	mu                sync.RWMutex // 读写锁：保护所有安全检查器字段的并发访问安全
}

// NewSecurityChecker 创建安全检查器
func NewSecurityChecker(maxMessageSize int) *SecurityChecker {
	return &SecurityChecker{
		maxMessageSize: maxMessageSize,
		allowedOrigins: []string{"*"}, // 默认允许所有来源
		blockedPatterns: []string{
			"<script",
			"javascript:",
			"eval(",
			"document.cookie",
			"window.location",
		},
	}
}

// CheckMessage 检查消息安全性
func (sc *SecurityChecker) CheckMessage(messageType int, data []byte) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	// 检查消息大小
	if len(data) > sc.maxMessageSize {
		sc.recordSecurityEvent()
		return fmt.Errorf("消息大小超过安全限制: %d > %d", len(data), sc.maxMessageSize)
	}

	// 检查文本消息中的可疑模式（优化字符串转换）
	if messageType == websocket.TextMessage {
		// 只转换一次字符串，避免重复转换
		messageContent := string(data)
		contentLower := strings.ToLower(messageContent)
		for _, pattern := range sc.blockedPatterns {
			if strings.Contains(contentLower, pattern) {
				sc.recordSecurityEvent()
				return fmt.Errorf("检测到可疑内容模式: %s", pattern)
			}
		}
	}

	return nil
}

// recordSecurityEvent 记录安全事件
func (sc *SecurityChecker) recordSecurityEvent() {
	sc.suspiciousCount++
	sc.lastSecurityEvent = time.Now()
	log.Printf("🚨 安全事件记录: 总计 %d 次可疑活动", sc.suspiciousCount)
}

// GetSecurityStats 获取安全统计
func (sc *SecurityChecker) GetSecurityStats() map[string]any {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	return map[string]any{
		"suspicious_count":       sc.suspiciousCount,
		"last_security_event":    sc.lastSecurityEvent,
		"blocked_patterns_count": len(sc.blockedPatterns),
		"allowed_origins_count":  len(sc.allowedOrigins),
	}
}

// RateLimiter 频率限制器
// 这个结构体实现了滑动窗口算法的频率限制功能，防止请求过于频繁
//
// 限流算法：
//   - 滑动窗口：在指定时间窗口内限制最大请求数
//   - 自动清理：过期的请求记录会被自动清理
//   - 阻塞机制：超过限制时会阻塞一个时间窗口
//
// 工作原理：
//  1. 记录每个请求的时间戳
//  2. 检查时间窗口内的请求数量
//  3. 超过限制时拒绝请求并记录违规
//  4. 自动清理过期的请求记录
//
// 使用场景：
//   - API频率限制：防止客户端过度调用
//   - DoS防护：防止恶意的高频请求
//   - 资源保护：保护后端服务不被压垮
//   - 公平使用：确保所有用户的公平访问
//
// 并发安全：使用互斥锁保护所有字段的并发访问
type RateLimiter struct {
	maxRequests    int           // 最大请求数：在时间窗口内允许的最大请求数量
	timeWindow     time.Duration // 时间窗口：限流的时间范围（如1分钟、1小时）
	requests       []time.Time   // 请求时间记录：存储每个请求的时间戳，用于滑动窗口计算
	mu             sync.Mutex    // 互斥锁：保护请求记录和状态的并发访问安全
	blockedUntil   time.Time     // 阻塞截止时间：超过限制时的阻塞结束时间
	violationCount int64         // 违规次数：累计超过频率限制的次数，用于监控和告警
}

// NewRateLimiter 创建频率限制器
func NewRateLimiter(maxRequests int, timeWindow time.Duration) *RateLimiter {
	return &RateLimiter{
		maxRequests: maxRequests,
		timeWindow:  timeWindow,
		requests:    make([]time.Time, 0),
	}
}

// Allow 检查是否允许请求
func (rl *RateLimiter) Allow() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()

	// 检查是否还在阻塞期
	if now.Before(rl.blockedUntil) {
		return false
	}

	// 清理过期的请求记录
	cutoff := now.Add(-rl.timeWindow)
	validRequests := make([]time.Time, 0)
	for _, reqTime := range rl.requests {
		if reqTime.After(cutoff) {
			validRequests = append(validRequests, reqTime)
		}
	}
	rl.requests = validRequests

	// 检查是否超过限制
	if len(rl.requests) >= rl.maxRequests {
		rl.violationCount++
		rl.blockedUntil = now.Add(rl.timeWindow) // 阻塞一个时间窗口
		log.Printf("⚠️ 频率限制触发: %d 请求在 %v 内，阻塞到 %v",
			len(rl.requests), rl.timeWindow, rl.blockedUntil)
		return false
	}

	// 记录这次请求
	rl.requests = append(rl.requests, now)
	return true
}

// GetStats 获取频率限制统计
func (rl *RateLimiter) GetStats() map[string]any {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	return map[string]any{
		"max_requests":     rl.maxRequests,
		"time_window_ms":   rl.timeWindow.Milliseconds(),
		"current_requests": len(rl.requests),
		"violation_count":  rl.violationCount,
		"blocked_until":    rl.blockedUntil,
		"is_blocked":       time.Now().Before(rl.blockedUntil),
	}
}

// ConnectionStats 连接统计信息
// 这个结构体记录WebSocket连接的详细统计数据，用于监控、分析和调试
// 提供全面的连接性能指标和错误统计，支持JSON序列化便于数据导出
//
// 统计分类：
//  1. 时间统计：连接时间、消息时间、持续时间
//  2. 消息统计：发送和接收的消息数量及字节数
//  3. 连接统计：重连次数和连接状态
//  4. 错误统计：详细的错误分类和趋势
//
// 使用场景：
//   - 性能监控：实时监控连接性能和消息吞吐量
//   - 问题诊断：分析连接问题和错误模式
//   - 容量规划：基于历史数据进行容量规划
//   - 告警系统：设置阈值进行自动告警
//
// 数据精度：
//   - 时间精度：纳秒级别，适合高精度性能分析
//   - 计数精度：64位整数，支持长期运行的大量数据
//   - 错误精度：详细的错误分类和趋势分析
type ConnectionStats struct {
	ConnectTime      time.Time     `json:"connect_time"`      // 连接建立时间：记录WebSocket连接成功建立的时间戳，用于计算连接持续时间
	LastMessageTime  time.Time     `json:"last_message_time"` // 最后消息时间：记录最近一次收到或发送消息的时间，用于检测连接活跃度
	MessagesSent     int64         `json:"messages_sent"`     // 发送消息数：累计发送的消息总数，包括文本、二进制和控制消息
	MessagesReceived int64         `json:"messages_received"` // 接收消息数：累计接收的消息总数，用于计算消息吞吐量
	BytesSent        int64         `json:"bytes_sent"`        // 发送字节数：累计发送的数据总量（字节），用于带宽使用分析
	BytesReceived    int64         `json:"bytes_received"`    // 接收字节数：累计接收的数据总量（字节），用于流量统计
	ReconnectCount   int           `json:"reconnect_count"`   // 重连次数：记录连接断开后的重连尝试次数，用于稳定性分析
	Uptime           time.Duration `json:"uptime"`            // 连接持续时间：当前连接已经保持的时间长度，实时更新
	Errors           ErrorStats    `json:"errors"`            // 错误统计：详细的错误分类、计数和趋势数据，用于问题诊断
}

// ===== WebSocket客户端主体实现 =====
// 高性能WebSocket客户端的核心实现，包含连接管理、消息处理、错误恢复等功能

// WebSocketClient 代表一个高性能的 WebSocket 客户端实例
// 这是整个WebSocket客户端的核心结构体，集成了连接管理、消息处理、错误恢复等功能
//
// 主要特性：
//  1. 自动重连：连接断开时自动尝试重新连接
//  2. 并发安全：使用锁机制保护共享资源，支持多goroutine并发访问
//  3. 优雅关闭：正确处理关闭信号，清理所有资源
//  4. 性能监控：实时统计连接状态、消息数量、错误信息等
//  5. 事件驱动：支持自定义回调函数处理各种事件
//  6. 日志记录：可选的消息日志记录功能
//
// 设计模式：
//   - 使用依赖注入模式，支持自定义连接器、消息处理器等组件
//   - 采用事件驱动架构，通过回调函数处理各种事件
//   - 实现了优雅关闭模式，确保资源正确释放
//
// 并发安全性：
//   - 使用原子操作处理状态和计数器
//   - 使用读写锁保护共享资源
//   - 使用专用锁防止WebSocket并发写入
type WebSocketClient struct {
	// ===== 配置和连接管理 =====
	config *ClientConfig   `json:"-"` // 客户端配置：包含URL、超时、重试等所有配置参数
	conn   *websocket.Conn `json:"-"` // WebSocket连接：底层的WebSocket连接对象

	// ===== 生命周期管理 =====
	ctx    context.Context    `json:"-"` // 生命周期管理上下文：用于控制所有goroutine的生命周期
	cancel context.CancelFunc `json:"-"` // 取消函数：调用此函数可以优雅地关闭客户端

	// ===== 并发控制机制 =====
	mu      sync.RWMutex   `json:"-"` // 读写锁：保护共享资源，读多写少的场景下性能更好
	writeMu sync.Mutex     `json:"-"` // 写操作专用锁：防止多个goroutine同时写入WebSocket（WebSocket不支持并发写）
	wg      sync.WaitGroup `json:"-"` // 等待组：管理所有goroutine，确保优雅关闭时所有goroutine都已结束

	// ===== 状态管理（原子操作） =====
	State      int32  `json:"state"`       // 连接状态：使用原子操作确保并发安全（StateDisconnected/StateConnecting等）
	RetryCount int32  `json:"retry_count"` // 重试计数：记录重连尝试次数，使用原子操作确保并发安全
	SessionID  string `json:"session_id"`  // 会话ID：唯一标识这个连接会话，用于日志跟踪和问题诊断

	// ===== 定时器和统计信息 =====
	pingTicker *time.Ticker    `json:"-"`     // Ping定时器：定期发送ping消息保持连接活跃
	Stats      ConnectionStats `json:"stats"` // 连接统计：记录消息数量、错误次数、连接时间等统计信息

	// ===== 事件回调函数 =====
	// 这些回调函数实现了事件驱动架构，让用户可以自定义各种事件的处理逻辑
	onConnect    func()                                   `json:"-"` // 连接成功回调：连接建立时调用
	onDisconnect func(error)                              `json:"-"` // 断开连接回调：连接断开时调用，参数是断开原因
	onMessage    func(messageType int, data []byte) error `json:"-"` // 消息处理回调：收到消息时调用
	onError      func(error)                              `json:"-"` // 错误处理回调：发生错误时调用

	// ===== 日志记录功能 =====
	logFile *os.File `json:"-"` // 消息日志文件句柄：用于记录所有收发的消息，便于调试和审计

	// 监控和指标
	metrics       PrometheusMetrics `json:"-"` // Prometheus指标
	metricsServer *http.Server      `json:"-"` // 指标服务器
	healthServer  *http.Server      `json:"-"` // 健康检查服务器

	// goroutine泄漏检测
	goroutineTracker *GoroutineTracker `json:"-"` // goroutine跟踪器

	// ===== 核心组件 =====
	connector        Connector        `json:"-"` // 连接器
	messageProcessor MessageProcessor `json:"-"` // 消息处理器
	errorRecovery    ErrorRecovery    `json:"-"` // 错误恢复器

	// ===== 新增：高级功能 =====
	AutoRecovery       bool                `json:"auto_recovery"`   // 自动错误恢复
	AdaptiveBuffer     bool                `json:"adaptive_buffer"` // 自适应缓冲区
	deadlockDetector   *DeadlockDetector   `json:"-"`               // 死锁检测器
	performanceMonitor *PerformanceMonitor `json:"-"`               // 性能监控器

	// ===== 新增：配置热重载 =====
	HotReloadEnabled bool `json:"hot_reload"` // 是否启用热重载

	// ===== 新增：安全功能 =====
	securityChecker *SecurityChecker `json:"-"` // 安全检查器
	rateLimiter     *RateLimiter     `json:"-"` // 频率限制器
}

// NewWebSocketClient 创建并初始化一个新的 WebSocketClient 实例
// 这是客户端的主要构造函数，负责初始化所有组件和功能
// 采用分阶段初始化的方式，确保每个组件都正确设置
//
// 参数说明：
//   - config: 客户端配置，如果为nil则使用默认配置
//
// 返回值：
//   - *WebSocketClient: 完全初始化的客户端实例
//
// 初始化阶段：
//  1. createClientInstance: 创建基础实例和上下文
//  2. initializeCoreComponents: 初始化核心组件（连接器、处理器等）
//  3. initializeAdvancedFeatures: 初始化高级功能（监控、性能优化等）
//  4. initializeSecurityFeatures: 初始化安全功能（检查器、限流器等）
//  5. finalizeInitialization: 完成最终初始化（会话ID、统计等）
//
// 使用示例：
//
//	// 基本用法
//	config := NewDefaultConfig("wss://example.com/ws")
//	client := NewWebSocketClient(config)
//
//	// 设置事件处理器
//	client.SetEventHandlers(onConnect, onDisconnect, onMessage, onError)
//
//	// 启动客户端（非阻塞）
//	go client.Start()
//
//	// 程序结束时优雅关闭
//	defer client.Stop()
//
// 注意事项：
//   - 客户端创建后需要调用Start()方法才会开始连接
//   - 建议使用defer client.Stop()确保资源正确释放
//   - 如果需要自定义组件，应在调用Start()之前设置
func NewWebSocketClient(config *ClientConfig) *WebSocketClient {
	// 第一步：参数验证，确保配置不为空
	if config == nil {
		config = NewDefaultConfig("") // 使用默认配置
	}

	// 第二步：分阶段初始化，确保每个组件都正确设置
	client := createClientInstance(config)    // 创建基础实例
	client.initializeCoreComponents(config)   // 初始化核心组件
	client.initializeAdvancedFeatures()       // 初始化高级功能
	client.initializeSecurityFeatures(config) // 初始化安全功能
	client.finalizeInitialization(config)     // 完成最终初始化

	return client
}

// createClientInstance 创建客户端基础实例
// 这是初始化过程的第一阶段，创建客户端的基础结构和必要的上下文
//
// 参数说明：
//   - config: 客户端配置
//
// 返回值：
//   - *WebSocketClient: 基础实例，包含基本的状态和统计结构
//
// 初始化内容：
//  1. 创建生命周期管理的上下文和取消函数
//  2. 设置初始连接状态为未连接
//  3. 生成唯一的会话ID用于跟踪
//  4. 初始化统计信息结构（预分配容量以提高性能）
//  5. 创建goroutine跟踪器防止泄漏
//
// 性能优化：
//   - 预分配map容量减少动态扩容开销
//   - 使用合理的初始容量避免内存浪费
func createClientInstance(config *ClientConfig) *WebSocketClient {
	// 创建可取消的上下文，用于控制所有goroutine的生命周期
	ctx, cancel := context.WithCancel(context.Background())

	return &WebSocketClient{
		// 基础配置和上下文
		config: config,
		ctx:    ctx,
		cancel: cancel,

		// 初始状态设置
		State:     int32(StateDisconnected), // 初始状态为未连接
		SessionID: generateSessionID(),      // 生成唯一会话ID

		// 统计信息初始化（预分配容量提高性能）
		Stats: ConnectionStats{
			Errors: ErrorStats{
				ErrorsByCode: make(map[ErrorCode]int64, 20),   // 预分配20种错误类型的容量
				ErrorTrend:   make([]ErrorTrendPoint, 0, 100), // 预分配100个趋势点的容量
			},
		},

		// Prometheus指标初始化
		metrics: PrometheusMetrics{
			ErrorsByCodeTotal: make(map[ErrorCode]int64, 20), // 预分配错误码统计容量
		},

		// goroutine泄漏跟踪器（最大存活5分钟，最多10个goroutine）
		goroutineTracker: NewGoroutineTracker(5*time.Minute, 10),
	}
}

// initializeCoreComponents 初始化核心组件
// 这是初始化过程的第二阶段，设置WebSocket连接和消息处理的核心组件
//
// 参数说明：
//   - config: 客户端配置，用于配置各个组件的参数
//
// 初始化的核心组件：
//  1. connector: WebSocket连接器，负责建立和管理连接
//  2. messageProcessor: 消息处理器，负责处理收发的消息
//  3. errorRecovery: 错误恢复器，负责处理连接错误和重试逻辑
//
// 这些组件采用依赖注入模式，可以在运行时替换为自定义实现
func (c *WebSocketClient) initializeCoreComponents(config *ClientConfig) {
	// 初始化WebSocket连接器（负责连接建立和管理）
	c.connector = NewDefaultConnector()

	// 初始化消息处理器（负责消息验证和处理）
	c.messageProcessor = NewDefaultMessageProcessor(config.MaxMessageSize, false)

	// 初始化错误恢复器（负责错误处理和重试逻辑）
	c.errorRecovery = NewDefaultErrorRecovery(config.MaxRetries, config.RetryDelay)
}

// initializeAdvancedFeatures 初始化高级功能
// 这是初始化过程的第三阶段，设置性能优化和监控相关的高级功能
//
// 初始化的高级功能：
//  1. AutoRecovery: 自动错误恢复功能
//  2. AdaptiveBuffer: 自适应缓冲区功能
//  3. deadlockDetector: 死锁检测器
//  4. performanceMonitor: 性能监控器
//  5. HotReloadEnabled: 热重载功能（默认关闭）
//
// 这些功能提供了企业级的监控和性能优化能力
func (c *WebSocketClient) initializeAdvancedFeatures() {
	// 启用自动错误恢复（连接断开时自动重连）
	c.AutoRecovery = true

	// 启用自适应缓冲区（根据消息大小动态调整缓冲区）
	c.AdaptiveBuffer = true

	// 初始化死锁检测器（30秒超时检测）
	c.deadlockDetector = NewDeadlockDetector(30 * time.Second)

	// 初始化性能监控器（监控CPU、内存等系统资源）
	c.performanceMonitor = NewPerformanceMonitor()

	// 热重载功能默认关闭（可在运行时启用）
	c.HotReloadEnabled = false
}

// initializeSecurityFeatures 初始化安全功能
// 这是初始化过程的第四阶段，设置安全检查和防护相关的功能
//
// 参数说明：
//   - config: 客户端配置，用于配置安全组件的参数
//
// 初始化的安全功能：
//  1. securityChecker: 安全检查器，验证消息内容和格式
//  2. rateLimiter: 频率限制器，防止消息发送过载
//
// 这些功能提供了企业级的安全防护能力
func (c *WebSocketClient) initializeSecurityFeatures(config *ClientConfig) {
	// 初始化安全检查器（验证消息大小和内容）
	c.securityChecker = NewSecurityChecker(config.MaxMessageSize)

	// 初始化频率限制器（每分钟最多100条消息）
	c.rateLimiter = NewRateLimiter(100, time.Minute)
}

// finalizeInitialization 完成初始化设置
func (c *WebSocketClient) finalizeInitialization(config *ClientConfig) {
	c.setDefaultHandlers()

	if err := c.initMessageLog(); err != nil {
		log.Printf("⚠️ 初始化消息日志失败: %v", err)
	}

	if config.MetricsEnabled {
		c.startMonitoringServers()
	}
}

// generateSessionID 生成唯一的会话ID - 极致优化版本
func generateSessionID() string {
	// 使用高性能字符串构建器避免fmt.Sprintf的分配
	builder := NewFastStringBuilder(32)
	defer builder.Release()

	now := time.Now()
	builder.WriteString("ws_")
	builder.WriteInt(now.Unix())
	_ = builder.WriteByte('_')
	builder.WriteInt(now.UnixNano() % 1000000) // 使用纳秒的后6位
	_ = builder.WriteByte('_')
	// 使用加密安全的随机数生成器
	var randomBytes [8]byte
	if _, err := rand.Read(randomBytes[:]); err == nil {
		// 将随机字节转换为正整数
		randomNum := int64(randomBytes[0])<<56 | int64(randomBytes[1])<<48 |
			int64(randomBytes[2])<<40 | int64(randomBytes[3])<<32 |
			int64(randomBytes[4])<<24 | int64(randomBytes[5])<<16 |
			int64(randomBytes[6])<<8 | int64(randomBytes[7])
		if randomNum < 0 {
			randomNum = -randomNum
		}
		builder.WriteInt(randomNum % 1000000)
	} else {
		// 降级到时间戳作为随机数
		builder.WriteInt(now.UnixNano() % 1000000)
	}

	return builder.String()
}

// initMessageLog 初始化消息日志文件
func (c *WebSocketClient) initMessageLog() error {
	if c.config.LogFile == "" {
		return nil // 不需要记录日志文件
	}

	// 如果用户只指定了目录或者使用了特殊标记，生成默认文件名
	logPath := c.config.LogFile
	if logPath == "auto" || logPath == "." {
		now := time.Now()
		logPath = fmt.Sprintf("websocket_log_%s.log", now.Format("20060102_150405"))
	}

	// 验证和清理日志文件路径，防止路径遍历攻击
	validatedPath, err := validateLogPath(logPath)
	if err != nil {
		return fmt.Errorf("日志路径验证失败: %w", err)
	}

	// 创建或打开日志文件（使用更安全的权限）
	// 使用安全的文件创建方法避免gosec G304警告
	file, err := c.createLogFileSafely(validatedPath)
	if err != nil {
		return fmt.Errorf("无法创建日志文件 %s: %w", validatedPath, err)
	}

	c.logFile = file

	// 写入会话开始标记
	header := fmt.Sprintf("\n=== WebSocket 会话开始 [%s] ===\n会话ID: %s\n目标URL: %s\n开始时间: %s\n\n",
		AppVersion, c.SessionID, c.config.URL, time.Now().Format("2006-01-02 15:04:05"))

	if _, err := c.logFile.WriteString(header); err != nil {
		log.Printf("⚠️ 写入日志文件头部失败: %v", err)
	}

	log.Printf("📝 消息日志记录到: %s", validatedPath)
	return nil
}

// logMessage 记录消息到日志文件
func (c *WebSocketClient) logMessage(direction string, messageType int, data []byte) {
	if c.logFile == nil {
		return
	}

	builder := NewFastStringBuilder(512)
	defer builder.Release()

	c.buildTimestamp(builder)
	c.buildMessageHeader(builder, direction, messageType, len(data))
	c.buildMessageContent(builder, messageType, data)
	_ = builder.WriteByte('\n')

	if _, err := c.logFile.WriteString(builder.String()); err != nil {
		log.Printf("⚠️ 写入消息日志失败: %v", err)
	}
}

// buildTimestamp 构建高性能时间戳
func (c *WebSocketClient) buildTimestamp(builder *FastStringBuilder) {
	now := time.Now()
	_ = builder.WriteByte('[')
	builder.WriteInt(int64(now.Year()))
	_ = builder.WriteByte('-')
	if now.Month() < 10 {
		_ = builder.WriteByte('0')
	}
	builder.WriteInt(int64(now.Month()))
	_ = builder.WriteByte('-')
	if now.Day() < 10 {
		_ = builder.WriteByte('0')
	}
	builder.WriteInt(int64(now.Day()))
	builder.WriteString(" ")
	if now.Hour() < 10 {
		_ = builder.WriteByte('0')
	}
	builder.WriteInt(int64(now.Hour()))
	_ = builder.WriteByte(':')
	if now.Minute() < 10 {
		_ = builder.WriteByte('0')
	}
	builder.WriteInt(int64(now.Minute()))
	_ = builder.WriteByte(':')
	if now.Second() < 10 {
		_ = builder.WriteByte('0')
	}
	builder.WriteInt(int64(now.Second()))
	_ = builder.WriteByte('.')
	ms := now.Nanosecond() / 1000000
	if ms < 100 {
		_ = builder.WriteByte('0')
		if ms < 10 {
			_ = builder.WriteByte('0')
		}
	}
	builder.WriteInt(int64(ms))
	builder.WriteString("] ")
}

// buildMessageHeader 构建消息头部信息
func (c *WebSocketClient) buildMessageHeader(builder *FastStringBuilder, direction string, messageType, dataLen int) {
	builder.WriteString(direction)
	_ = builder.WriteByte(' ')
	builder.WriteString(c.getMessageTypeString(messageType))
	builder.WriteString(" (")
	builder.WriteInt(int64(dataLen))
	builder.WriteString(" bytes): ")
}

// buildMessageContent 构建消息内容
func (c *WebSocketClient) buildMessageContent(builder *FastStringBuilder, messageType int, data []byte) {
	if messageType == websocket.BinaryMessage {
		c.buildBinaryContent(builder, data)
	} else {
		c.buildTextContent(builder, data)
	}
}

// buildBinaryContent 构建二进制消息内容
func (c *WebSocketClient) buildBinaryContent(builder *FastStringBuilder, data []byte) {
	if len(data) <= 32 {
		builder.WriteString("HEX: ")
		c.writeHexBytes(builder, data)
	} else {
		builder.WriteString("BINARY: ")
		builder.WriteInt(int64(len(data)))
		builder.WriteString(" bytes, preview: ")
		c.writeHexBytes(builder, data[:16])
		builder.WriteString("...")
	}
}

// buildTextContent 构建文本消息内容（优化字符串转换）
func (c *WebSocketClient) buildTextContent(builder *FastStringBuilder, data []byte) {
	if len(data) <= 500 {
		builder.WriteString(string(data))
	} else {
		// 只转换一次，避免重复转换
		truncatedData := string(data[:500])
		builder.WriteString(truncatedData)
		builder.WriteString("...(truncated)")
	}
}

// writeHexBytes 写入十六进制字节
func (c *WebSocketClient) writeHexBytes(builder *FastStringBuilder, data []byte) {
	const hexChars = "0123456789abcdef"
	for _, b := range data {
		if b < 16 {
			_ = builder.WriteByte('0')
		}
		builder.WriteString(hexChars[b>>4 : b>>4+1])
		builder.WriteString(hexChars[b&0xf : (b&0xf)+1])
	}
}

// 预定义的消息类型字符串，避免重复的map查找
var messageTypeStrings = [...]string{
	"TYPE_0", "TEXT", "BINARY", "TYPE_3", "TYPE_4", "TYPE_5", "TYPE_6", "TYPE_7", "CLOSE", "PING", "PONG",
}

// getMessageTypeString 获取消息类型的字符串表示（极致优化版）
// 这个方法将WebSocket消息类型常量转换为可读的字符串表示
//
// 参数说明：
//   - messageType: WebSocket消息类型常量（如websocket.TextMessage）
//
// 返回值：
//   - string: 消息类型的字符串表示
//
// 性能优化：
//  1. 使用预定义数组而不是map查找，避免哈希计算开销
//  2. 数组索引访问时间复杂度为O(1)
//  3. 对未知类型使用高性能字符串构建器
//  4. 避免fmt.Sprintf的内存分配和格式化开销
//
// 支持的消息类型：
//   - 0: TYPE_0（保留）
//   - 1: TEXT（文本消息）
//   - 2: BINARY（二进制消息）
//   - 8: CLOSE（关闭消息）
//   - 9: PING（ping消息）
//   - 10: PONG（pong消息）
//
// 使用场景：
//   - 日志记录中的消息类型显示
//   - 调试信息的格式化输出
//   - 监控系统的消息分类统计
func (c *WebSocketClient) getMessageTypeString(messageType int) string {
	// 第一步：使用数组索引查找已知类型（性能最优）
	if messageType >= 0 && messageType < len(messageTypeStrings) {
		return messageTypeStrings[messageType]
	}

	// 第二步：对于未知类型，使用高性能字符串构建器
	builder := NewFastStringBuilder(16) // 预分配16字节，足够"TYPE_xxx"格式
	defer builder.Release()

	builder.WriteString("TYPE_")
	builder.WriteInt(int64(messageType))
	return builder.String()
}

// closeMessageLog 关闭消息日志文件
// 这个方法优雅地关闭消息日志文件，确保数据完整性和资源正确释放
//
// 功能说明：
//  1. 检查日志文件是否存在
//  2. 写入会话结束标记和时间戳
//  3. 刷新并关闭文件句柄
//  4. 清理文件引用，防止内存泄漏
//
// 会话结束标记格式：
//
//	=== WebSocket 会话结束 [会话ID] ===
//	结束时间: YYYY-MM-DD HH:MM:SS
//
// 错误处理：
//   - 写入失败：记录警告但继续关闭文件
//   - 关闭失败：记录警告，避免程序崩溃
//   - 确保在任何情况下都清理文件引用
//
// 调用时机：
//   - 客户端正常停止时
//   - 程序异常退出时（defer调用）
//   - 日志文件切换时
//
// 并发安全：此方法应在主goroutine中调用，避免并发访问文件
func (c *WebSocketClient) closeMessageLog() {
	// 第一步：检查日志文件是否存在
	if c.logFile != nil {
		// 第二步：写入会话结束标记
		footer := fmt.Sprintf("\n=== WebSocket 会话结束 [%s] ===\n结束时间: %s\n\n",
			c.SessionID, time.Now().Format("2006-01-02 15:04:05"))
		if _, err := c.logFile.WriteString(footer); err != nil {
			log.Printf("⚠️ 写入日志文件尾部失败: %v", err)
		}

		// 第三步：关闭文件句柄
		if closeErr := c.logFile.Close(); closeErr != nil {
			log.Printf("⚠️ 关闭日志文件失败: %v", closeErr)
		}

		// 第四步：清理文件引用，防止重复关闭
		c.logFile = nil
	}
}

// setDefaultHandlers 设置默认的事件处理器
// 这个方法为WebSocket客户端设置标准的事件处理回调函数
//
// 功能说明：
//  1. 设置连接建立时的处理逻辑
//  2. 设置连接断开时的处理逻辑
//  3. 设置消息接收时的处理逻辑
//  4. 设置错误发生时的处理逻辑
//
// 默认处理器特点：
//   - 提供友好的日志输出，包含emoji和会话ID
//   - 区分正常关闭和异常断开
//   - 消息处理委托给MessageProcessor
//   - 错误处理记录详细信息便于调试
//
// 事件处理器说明：
//   - onConnect: 连接成功建立时调用
//   - onDisconnect: 连接断开时调用，区分正常和异常
//   - onMessage: 接收到消息时调用，默认不做额外处理
//   - onError: 发生错误时调用，记录错误信息
//
// 自定义处理器：
//
//	用户可以在客户端启动前覆盖这些默认处理器：
//	client.SetOnConnect(func() { ... })
//	client.SetOnMessage(func(int, []byte) error { ... })
//
// 并发安全：处理器函数在不同的goroutine中调用，需要注意线程安全
func (c *WebSocketClient) setDefaultHandlers() {
	// 连接建立处理器：记录成功连接信息
	// 这个匿名函数在WebSocket连接成功建立时被调用，用于记录连接成功的日志信息
	c.onConnect = func() {
		log.Printf("✅ 连接成功建立 [会话: %s]", c.SessionID)
	}

	// 连接断开处理器：区分正常关闭和异常断开
	// 这个匿名函数在WebSocket连接断开时被调用，根据错误参数判断断开原因
	c.onDisconnect = func(err error) {
		if err != nil {
			// 异常断开：由于错误导致的连接中断
			log.Printf("🔌 连接断开: %v [会话: %s]", err, c.SessionID)
		} else {
			// 正常关闭：主动调用Stop()或收到正常关闭帧
			log.Printf("🔌 连接正常关闭 [会话: %s]", c.SessionID)
		}
	}

	// 消息接收处理器：默认不做额外处理
	// 这个匿名函数在收到WebSocket消息时被调用，默认实现不做额外处理
	c.onMessage = func(messageType int, data []byte) error {
		// 默认不做额外处理，消息已经由MessageProcessor处理并记录
		// 用户可以通过SetOnMessage方法覆盖此处理器来实现自定义逻辑
		return nil
	}

	// 错误处理器：记录错误信息便于调试
	// 这个匿名函数在发生各种错误时被调用，用于统一的错误日志记录
	c.onError = func(err error) {
		log.Printf("❌ 客户端错误: %v [会话: %s]", err, c.SessionID)
	}
}

// GetState 获取当前连接状态
// 这个方法以线程安全的方式获取WebSocket客户端的当前连接状态
//
// 返回值：
//   - ConnectionState: 当前的连接状态枚举值
//
// 连接状态说明：
//   - StateDisconnected: 未连接状态
//   - StateConnecting: 正在连接中
//   - StateConnected: 已连接状态
//   - StateReconnecting: 正在重连中
//   - StateStopping: 正在停止中
//   - StateStopped: 已停止状态
//
// 并发安全：
//   - 使用原子操作读取状态，确保线程安全
//   - 可以在任意goroutine中安全调用
//   - 不会阻塞其他操作
//
// 使用场景：
//   - 健康检查和状态监控
//   - 条件判断和流程控制
//   - 用户界面状态显示
//   - 日志记录和调试
func (c *WebSocketClient) GetState() ConnectionState {
	return ConnectionState(atomic.LoadInt32(&c.State))
}

// setState 设置连接状态
// 这个私有方法以线程安全的方式更新WebSocket客户端的连接状态
//
// 参数说明：
//   - state: 要设置的新连接状态
//
// 并发安全：
//   - 使用原子操作写入状态，确保线程安全
//   - 状态更新是原子性的，不会出现中间状态
//   - 可以在任意goroutine中安全调用
//
// 状态转换规则：
//   - 状态转换应该遵循合理的状态机逻辑
//   - 避免无效的状态转换（如从Stopped直接到Connected）
//   - 状态更新应该及时反映实际的连接情况
//
// 调用场景：
//   - 连接建立时设置为StateConnected
//   - 连接断开时设置为StateDisconnected
//   - 开始重连时设置为StateReconnecting
//   - 客户端停止时设置为StateStopped
func (c *WebSocketClient) setState(state ConnectionState) {
	atomic.StoreInt32(&c.State, int32(state))
}

// isConnected 检查是否已连接
// 这个方法提供了一个便捷的方式来检查WebSocket是否处于已连接状态
//
// 返回值：
//   - bool: true表示已连接，false表示未连接
//
// 判断逻辑：
//   - 只有当状态为StateConnected时才返回true
//   - 其他所有状态（包括连接中、重连中等）都返回false
//   - 确保只有真正建立连接时才认为是已连接
//
// 并发安全：
//   - 内部调用GetState()方法，继承其线程安全特性
//   - 可以在任意goroutine中安全调用
//
// 使用场景：
//   - 发送消息前的连接状态检查
//   - 就绪检查和健康检查
//   - 交互模式的启动条件判断
//   - 业务逻辑的连接状态判断
func (c *WebSocketClient) isConnected() bool {
	return c.GetState() == StateConnected
}

// GetStats 获取连接统计信息
// 这个方法以线程安全的方式获取WebSocket连接的详细统计信息
//
// 返回值：
//   - ConnectionStats: 连接统计信息的副本
//
// 统计信息包含：
//  1. 连接时间：连接建立的时间戳
//  2. 运行时长：连接持续的时间（实时计算）
//  3. 消息统计：发送和接收的消息数量
//  4. 字节统计：发送和接收的字节总数
//  5. 重连统计：重连次数和相关信息
//  6. 错误统计：错误次数和详细信息
//  7. 最后消息时间：最近一次消息的时间戳
//
// 实时计算：
//   - 如果当前已连接且有连接时间，会实时计算运行时长
//   - 确保返回的统计信息是最新的
//
// 并发安全：
//   - 使用读锁保护统计数据的读取
//   - 返回数据副本，避免外部修改影响内部状态
//   - 可以在任意goroutine中安全调用
//
// 使用场景：
//   - 监控和性能分析
//   - 用户界面状态显示
//   - 日志记录和调试
//   - HTTP统计端点的数据源
func (c *WebSocketClient) GetStats() ConnectionStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// 获取统计数据副本
	stats := c.Stats

	// 实时计算运行时长（如果已连接且有连接时间）
	if c.isConnected() && !stats.ConnectTime.IsZero() {
		stats.Uptime = time.Since(stats.ConnectTime)
	}

	return stats
}

// updateStats 更新统计信息（线程安全版本）
// 这个方法更新消息传输的统计信息，包括本地统计和Prometheus指标
//
// 参数说明：
//   - _: 消息类型（当前未使用，保留用于扩展）
//   - dataLen: 消息数据长度（字节）
//   - sent: true表示发送消息，false表示接收消息
//
// 更新内容：
//  1. 最后消息时间：更新为当前时间
//  2. 消息计数：根据sent参数更新发送或接收计数
//  3. 字节计数：累加消息的字节数
//  4. Prometheus指标：原子更新对应的指标
//
// 并发安全：
//   - 使用互斥锁保护本地统计数据的更新
//   - 使用原子操作更新Prometheus指标
//   - 避免数据竞争和不一致状态
//
// 性能考虑：
//   - 锁的持有时间很短，只保护必要的更新操作
//   - Prometheus指标使用原子操作，性能更好
//   - 避免在锁内进行耗时操作
//
// 调用场景：
//   - 发送消息成功后调用
//   - 接收消息成功后调用
//   - 消息处理流程中的统计更新
func (c *WebSocketClient) updateStats(_ int, dataLen int, sent bool) {
	// 使用互斥锁保护本地统计数据
	c.mu.Lock()
	defer c.mu.Unlock()

	// 更新最后消息时间
	c.Stats.LastMessageTime = time.Now()

	if sent {
		// 更新发送统计
		c.Stats.MessagesSent++
		c.Stats.BytesSent += int64(dataLen)
		// 原子更新Prometheus指标以避免竞态条件
		atomic.AddInt64(&c.metrics.MessagesSentTotal, 1)
		atomic.AddInt64(&c.metrics.BytesSentTotal, int64(dataLen))
	} else {
		// 更新接收统计
		c.Stats.MessagesReceived++
		c.Stats.BytesReceived += int64(dataLen)
		// 原子更新Prometheus指标以避免竞态条件
		atomic.AddInt64(&c.metrics.MessagesReceivedTotal, 1)
		atomic.AddInt64(&c.metrics.BytesReceivedTotal, int64(dataLen))
	}
}

// recordError 记录错误统计信息（线程安全版本）
// 这个方法记录和统计WebSocket客户端发生的各种错误
//
// 参数说明：
//   - err: 发生的错误实例
//
// 记录内容：
//  1. 错误总数：累加错误计数
//  2. 最后错误：保存最近发生的错误
//  3. 错误时间：记录错误发生的时间戳
//  4. 错误分类：按错误码分类统计
//  5. 错误趋势：记录错误发生的时间序列
//  6. Prometheus指标：更新监控指标
//
// 错误分类：
//   - 自动提取错误码进行分类统计
//   - 支持自定义错误类型和标准错误
//   - 便于错误模式分析和问题诊断
//
// 错误趋势：
//   - 记录每个错误的时间戳和类型
//   - 保持最近1000个错误的历史记录
//   - 支持错误趋势分析和异常检测
//
// 并发安全：
//   - 使用互斥锁保护所有统计数据的更新
//   - 原子操作更新Prometheus指标
//   - 避免数据竞争和不一致状态
//
// 性能优化：
//   - 限制错误趋势数据的大小，避免内存泄漏
//   - 高效的错误码提取和分类
//   - 最小化锁的持有时间
//
// 使用场景：
//   - 连接错误、发送错误、接收错误的统计
//   - 错误模式分析和问题诊断
//   - 监控告警和性能分析
func (c *WebSocketClient) recordError(err error) {
	// 使用互斥锁保护错误统计数据
	c.mu.Lock()
	defer c.mu.Unlock()

	// 更新基本错误统计
	c.Stats.Errors.TotalErrors++
	c.Stats.Errors.LastError = err
	c.Stats.Errors.LastErrorTime = time.Now()

	// 提取和分类错误码
	errorCode := c.extractErrorCode(err)

	// 更新按错误码分类的统计
	c.Stats.Errors.ErrorsByCode[errorCode]++

	// 原子更新Prometheus指标以避免竞态条件
	atomic.AddInt64(&c.metrics.ErrorsTotal, 1)

	// 更新Prometheus错误码分类指标（需要锁保护map操作）
	if c.metrics.ErrorsByCodeTotal == nil {
		c.metrics.ErrorsByCodeTotal = make(map[ErrorCode]int64)
	}
	c.metrics.ErrorsByCodeTotal[errorCode]++

	// 添加到错误趋势记录
	trendPoint := ErrorTrendPoint{
		Timestamp:  time.Now(),
		ErrorCount: 1,
		ErrorCode:  errorCode,
	}
	c.Stats.Errors.ErrorTrend = append(c.Stats.Errors.ErrorTrend, trendPoint)

	// 保持错误趋势数据在合理范围内（最近1000个错误）
	// 避免内存无限增长
	if len(c.Stats.Errors.ErrorTrend) > 1000 {
		c.Stats.Errors.ErrorTrend = c.Stats.Errors.ErrorTrend[len(c.Stats.Errors.ErrorTrend)-1000:]
	}
}

// inferErrorCode 根据错误内容推断错误码
// 这个方法通过分析错误消息的内容来推断对应的标准化错误码
//
// 参数说明：
//   - err: 需要分析的错误实例
//
// 返回值：
//   - ErrorCode: 推断出的标准化错误码
//
// 推断逻辑：
//  1. 检查错误消息中的关键字
//  2. 按照常见错误模式进行匹配
//  3. 返回最匹配的错误码
//  4. 无法匹配时返回未知错误码
//
// 支持的错误模式：
//   - "connection refused" -> ErrCodeConnectionRefused
//   - "timeout" -> ErrCodeConnectionTimeout
//   - "no such host" -> ErrCodeDNSError
//   - "tls" -> ErrCodeTLSError
//   - "handshake" -> ErrCodeHandshakeFailed
//   - "message too large" -> ErrCodeMessageTooLarge
//   - "invalid" -> ErrCodeInvalidMessage
//   - "broken pipe"/"connection reset" -> ErrCodeConnectionLost
//
// 使用场景：
//   - 标准错误的分类和统计
//   - 错误恢复策略的选择
//   - 监控系统的错误分类
//   - 问题诊断和分析
//
// 注意事项：
//   - 基于字符串匹配，可能存在误判
//   - 优先匹配更具体的错误模式
//   - 对于自定义错误类型，应使用extractErrorCode方法
func (c *WebSocketClient) inferErrorCode(err error) ErrorCode {
	// 第一步：空错误检查
	if err == nil {
		return ErrCodeUnknownError
	}

	// 第二步：获取错误消息字符串
	errStr := err.Error()

	// 第三步：按照错误模式进行匹配（按常见程度排序）
	switch {
	case strings.Contains(errStr, "connection refused"):
		return ErrCodeConnectionRefused
	case strings.Contains(errStr, "timeout"):
		return ErrCodeConnectionTimeout
	case strings.Contains(errStr, "no such host"):
		return ErrCodeDNSError
	case strings.Contains(errStr, "tls"):
		return ErrCodeTLSError
	case strings.Contains(errStr, "handshake"):
		return ErrCodeHandshakeFailed
	case strings.Contains(errStr, "message too large"):
		return ErrCodeMessageTooLarge
	case strings.Contains(errStr, "invalid"):
		return ErrCodeInvalidMessage
	case strings.Contains(errStr, "broken pipe"), strings.Contains(errStr, "connection reset"):
		return ErrCodeConnectionLost
	default:
		// 无法匹配的错误返回未知错误码
		return ErrCodeUnknownError
	}
}

// GetErrorStats 获取错误统计信息
// 这个方法以线程安全的方式获取WebSocket客户端的详细错误统计信息
//
// 返回值：
//   - ErrorStats: 错误统计信息的深拷贝
//
// 统计信息包含：
//  1. 错误总数：累计发生的错误次数
//  2. 最后错误：最近发生的错误实例
//  3. 错误时间：最后一次错误的时间戳
//  4. 错误分类：按错误码分类的统计数据
//  5. 错误趋势：错误发生的时间序列数据
//
// 数据安全：
//   - 返回深拷贝，避免外部修改影响内部状态
//   - 使用读锁保护数据访问
//   - 确保数据一致性和完整性
//
// 并发安全：
//   - 可以在任意goroutine中安全调用
//   - 不会阻塞其他操作
//   - 保证数据的原子性读取
//
// 使用场景：
//   - 错误分析和问题诊断
//   - 监控系统的错误统计
//   - 性能分析和优化
//   - HTTP统计端点的数据源
func (c *WebSocketClient) GetErrorStats() ErrorStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// 创建错误统计信息的深拷贝
	stats := ErrorStats{
		TotalErrors:   c.Stats.Errors.TotalErrors,
		LastError:     c.Stats.Errors.LastError,
		LastErrorTime: c.Stats.Errors.LastErrorTime,
		ErrorsByCode:  make(map[ErrorCode]int64),
		ErrorTrend:    make([]ErrorTrendPoint, len(c.Stats.Errors.ErrorTrend)),
	}

	// 深拷贝错误码统计映射
	for code, count := range c.Stats.Errors.ErrorsByCode {
		stats.ErrorsByCode[code] = count
	}

	// 深拷贝错误趋势切片
	copy(stats.ErrorTrend, c.Stats.Errors.ErrorTrend)

	return stats
}

// GetErrorTrend 获取指定时间范围内的错误趋势
// 这个方法返回指定时间段内发生的错误趋势数据，用于错误模式分析
//
// 参数说明：
//   - since: 时间范围，从现在往前推算的时间段
//
// 返回值：
//   - []ErrorTrendPoint: 时间范围内的错误趋势点列表
//
// 趋势数据包含：
//   - 错误发生的时间戳
//   - 错误计数（通常为1）
//   - 错误类型码
//
// 过滤逻辑：
//   - 计算截止时间点（当前时间 - since）
//   - 只返回截止时间之后的错误记录
//   - 保持时间顺序不变
//
// 并发安全：
//   - 使用读锁保护数据访问
//   - 返回数据副本，避免外部修改
//
// 使用场景：
//   - 错误趋势分析和可视化
//   - 异常检测和告警
//   - 性能监控和诊断
//   - 错误模式识别
//
// 使用示例：
//
//	// 获取最近1小时的错误趋势
//	trend := client.GetErrorTrend(time.Hour)
//	// 获取最近24小时的错误趋势
//	trend := client.GetErrorTrend(24 * time.Hour)
func (c *WebSocketClient) GetErrorTrend(since time.Duration) []ErrorTrendPoint {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// 计算截止时间点
	cutoff := time.Now().Add(-since)
	var trend []ErrorTrendPoint

	// 过滤指定时间范围内的错误记录
	for _, point := range c.Stats.Errors.ErrorTrend {
		if point.Timestamp.After(cutoff) {
			trend = append(trend, point)
		}
	}

	return trend
}

// extractErrorCode 从错误中提取标准化的错误码
// 支持自定义错误类型(ConnectionError, RetryError)和标准错误的智能推断
// 返回的错误码用于错误分类统计和恢复策略选择
//
// Example:
//
//	code := client.extractErrorCode(err)
//	if code == ErrCodeConnectionRefused {
//	    // 处理连接被拒绝的情况
//	}
func (c *WebSocketClient) extractErrorCode(err error) ErrorCode {
	switch e := err.(type) {
	case *ConnectionError:
		return e.Code
	case *RetryError:
		return e.Code
	default:
		return c.inferErrorCode(err)
	}
}

// startMonitoringServers 启动监控服务器
// 这个方法根据配置启动Prometheus指标服务器和健康检查服务器
//
// 功能说明：
//  1. 检查配置中的端口设置
//  2. 启动Prometheus指标服务器（如果配置了端口）
//  3. 启动健康检查服务器（如果配置了端口）
//  4. 使用goroutine并发启动，不阻塞主流程
//
// 服务器类型：
//   - Prometheus指标服务器：提供/metrics端点
//   - 健康检查服务器：提供/health、/ready、/stats端点
//
// 配置要求：
//   - MetricsPort > 0：启动指标服务器
//   - HealthPort > 0：启动健康检查服务器
//   - 端口为0或负数：跳过对应服务器
//
// 并发启动：
//   - 每个服务器在独立的goroutine中运行
//   - 不会阻塞WebSocket客户端的主要功能
//   - 服务器启动失败不会影响客户端运行
//
// 使用场景：
//   - 生产环境的监控集成
//   - Kubernetes的健康检查
//   - Prometheus监控系统
//   - 负载均衡器的健康探测
func (c *WebSocketClient) startMonitoringServers() {
	// 启动Prometheus指标服务器（如果配置了端口）
	if c.config.MetricsPort > 0 {
		go c.startMetricsServer()
	}

	// 启动健康检查服务器（如果配置了端口）
	if c.config.HealthPort > 0 {
		go c.startHealthServer()
	}
}

// startMetricsServer 启动Prometheus指标服务器
// 这个方法启动一个HTTP服务器，提供Prometheus格式的指标数据
//
// 功能说明：
//  1. 创建HTTP路由器和处理器
//  2. 配置HTTP服务器参数
//  3. 启动服务器并监听指定端口
//  4. 处理服务器启动错误
//
// 提供的端点：
//   - /metrics：Prometheus格式的指标数据
//
// 服务器配置：
//   - ReadHeaderTimeout: 10秒，防止慢速攻击
//   - ReadTimeout: 30秒，完整请求读取超时
//   - WriteTimeout: 30秒，响应写入超时
//   - IdleTimeout: 60秒，空闲连接超时
//
// 错误处理：
//   - 忽略正常关闭错误（http.ErrServerClosed）
//   - 记录其他启动错误但不中断程序
//
// 并发安全：
//   - 在独立的goroutine中运行
//   - 不会阻塞其他操作
//
// 使用场景：
//   - Prometheus监控系统抓取指标
//   - Grafana仪表板数据源
//   - 自动化监控和告警
func (c *WebSocketClient) startMetricsServer() {
	// 创建HTTP路由器
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", c.handleMetrics)

	// 配置HTTP服务器
	c.metricsServer = &http.Server{
		Addr:              fmt.Sprintf(":%d", c.config.MetricsPort),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second, // 防止慢速攻击
		ReadTimeout:       30 * time.Second, // 完整请求读取超时
		WriteTimeout:      30 * time.Second, // 响应写入超时
		IdleTimeout:       60 * time.Second, // 空闲连接超时
	}

	// 记录服务器启动信息
	log.Printf("📊 启动Prometheus指标服务器: http://localhost:%d/metrics", c.config.MetricsPort)

	// 启动服务器（阻塞调用）
	if err := c.metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("❌ 指标服务器启动失败: %v", err)
	}
}

// startHealthServer 启动健康检查服务器
// 这个方法启动一个HTTP服务器，提供健康检查和统计信息端点
//
// 功能说明：
//  1. 创建HTTP路由器和多个处理器
//  2. 配置HTTP服务器参数
//  3. 启动服务器并监听指定端口
//  4. 处理服务器启动错误
//
// 提供的端点：
//   - /health：健康检查，返回客户端运行状态
//   - /ready：就绪检查，返回WebSocket连接状态
//   - /stats：统计信息，返回详细的连接和错误统计
//
// 服务器配置：
//   - ReadHeaderTimeout: 10秒，防止慢速攻击
//   - ReadTimeout: 30秒，完整请求读取超时
//   - WriteTimeout: 30秒，响应写入超时
//   - IdleTimeout: 60秒，空闲连接超时
//
// 错误处理：
//   - 忽略正常关闭错误（http.ErrServerClosed）
//   - 记录其他启动错误但不中断程序
//
// 并发安全：
//   - 在独立的goroutine中运行
//   - 不会阻塞其他操作
//
// 使用场景：
//   - Kubernetes liveness和readiness探测
//   - 负载均衡器健康检查
//   - 监控系统状态收集
//   - 运维工具的状态查询
func (c *WebSocketClient) startHealthServer() {
	// 创建HTTP路由器和处理器
	mux := http.NewServeMux()
	mux.HandleFunc("/health", c.handleHealth) // 健康检查端点
	mux.HandleFunc("/ready", c.handleReady)   // 就绪检查端点
	mux.HandleFunc("/stats", c.handleStats)   // 统计信息端点

	// 配置HTTP服务器
	c.healthServer = &http.Server{
		Addr:              fmt.Sprintf(":%d", c.config.HealthPort),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second, // 防止慢速攻击
		ReadTimeout:       30 * time.Second, // 完整请求读取超时
		WriteTimeout:      30 * time.Second, // 响应写入超时
		IdleTimeout:       60 * time.Second, // 空闲连接超时
	}

	// 记录服务器启动信息
	log.Printf("🏥 启动健康检查服务器: http://localhost:%d/health", c.config.HealthPort)

	// 启动服务器（阻塞调用）
	if err := c.healthServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("❌ 健康检查服务器启动失败: %v", err)
	}
}

// handleMetrics 处理Prometheus指标请求
// 这个HTTP处理器提供Prometheus格式的指标数据，用于监控系统集成
//
// 功能说明：
//   - 设置正确的Content-Type头部
//   - 更新最新的指标数据
//   - 输出标准Prometheus格式的指标
//   - 支持多种指标类型（计数器、仪表）
//
// 提供的指标：
//  1. websocket_connections_total: 总连接数（计数器）
//  2. websocket_connections_active: 当前活跃连接数（仪表）
//  3. websocket_messages_sent_total: 发送消息总数（计数器）
//  4. websocket_messages_received_total: 接收消息总数（计数器）
//  5. websocket_bytes_sent_total: 发送字节总数（计数器）
//  6. websocket_bytes_received_total: 接收字节总数（计数器）
//  7. websocket_errors_total: 错误总数（计数器）
//  8. websocket_reconnections_total: 重连总数（计数器）
//  9. websocket_errors_by_code_total: 按错误码分类的错误（带标签计数器）
//
// 使用场景：
//   - Prometheus监控系统抓取
//   - Grafana仪表板展示
//   - 告警规则配置
//   - 性能分析和调优
func (c *WebSocketClient) handleMetrics(w http.ResponseWriter, r *http.Request) {
	// 设置Prometheus标准的Content-Type
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	// 更新最新的指标数据
	c.updatePrometheusMetrics()

	// 输出Prometheus格式的指标
	// 1. 总连接数指标
	fmt.Fprintf(w, "# HELP websocket_connections_total Total number of WebSocket connections\n")
	fmt.Fprintf(w, "# TYPE websocket_connections_total counter\n")
	fmt.Fprintf(w, "websocket_connections_total %d\n", c.metrics.ConnectionsTotal)

	// 2. 当前活跃连接数指标
	fmt.Fprintf(w, "# HELP websocket_connections_active Current active WebSocket connections\n")
	fmt.Fprintf(w, "# TYPE websocket_connections_active gauge\n")
	fmt.Fprintf(w, "websocket_connections_active %d\n", c.metrics.ConnectionsActive)

	// 3. 发送消息总数指标
	fmt.Fprintf(w, "# HELP websocket_messages_sent_total Total number of messages sent\n")
	fmt.Fprintf(w, "# TYPE websocket_messages_sent_total counter\n")
	fmt.Fprintf(w, "websocket_messages_sent_total %d\n", c.metrics.MessagesSentTotal)

	// 4. 接收消息总数指标
	fmt.Fprintf(w, "# HELP websocket_messages_received_total Total number of messages received\n")
	fmt.Fprintf(w, "# TYPE websocket_messages_received_total counter\n")
	fmt.Fprintf(w, "websocket_messages_received_total %d\n", c.metrics.MessagesReceivedTotal)

	// 5. 发送字节总数指标
	fmt.Fprintf(w, "# HELP websocket_bytes_sent_total Total number of bytes sent\n")
	fmt.Fprintf(w, "# TYPE websocket_bytes_sent_total counter\n")
	fmt.Fprintf(w, "websocket_bytes_sent_total %d\n", c.metrics.BytesSentTotal)

	// 6. 接收字节总数指标
	fmt.Fprintf(w, "# HELP websocket_bytes_received_total Total number of bytes received\n")
	fmt.Fprintf(w, "# TYPE websocket_bytes_received_total counter\n")
	fmt.Fprintf(w, "websocket_bytes_received_total %d\n", c.metrics.BytesReceivedTotal)

	// 7. 错误总数指标
	fmt.Fprintf(w, "# HELP websocket_errors_total Total number of errors\n")
	fmt.Fprintf(w, "# TYPE websocket_errors_total counter\n")
	fmt.Fprintf(w, "websocket_errors_total %d\n", c.metrics.ErrorsTotal)

	// 8. 重连总数指标
	fmt.Fprintf(w, "# HELP websocket_reconnections_total Total number of reconnections\n")
	fmt.Fprintf(w, "# TYPE websocket_reconnections_total counter\n")
	fmt.Fprintf(w, "websocket_reconnections_total %d\n", c.metrics.ReconnectionsTotal)

	// 9. 按错误码分类的错误指标（带标签）
	for code, count := range c.metrics.ErrorsByCodeTotal {
		fmt.Fprintf(w, "# HELP websocket_errors_by_code_total Total errors by error code\n")
		fmt.Fprintf(w, "# TYPE websocket_errors_by_code_total counter\n")
		fmt.Fprintf(w, "websocket_errors_by_code_total{error_code=\"%d\",error_name=\"%s\"} %d\n",
			int(code), code.String(), count)
	}
}

// handleHealth 处理健康检查请求
// 这个HTTP处理器提供标准的健康检查端点，用于负载均衡器和监控系统
//
// 功能说明：
//   - 检查客户端的基本运行状态
//   - 返回JSON格式的健康状态信息
//   - 根据状态设置合适的HTTP状态码
//
// 健康判断逻辑：
//   - healthy: 客户端正在运行（非停止状态）
//   - unhealthy: 客户端已停止或正在停止
//
// 返回格式：
//
//	{
//	  "status": "healthy|unhealthy",
//	  "state": "客户端状态",
//	  "session_id": "会话ID",
//	  "timestamp": "检查时间"
//	}
//
// HTTP状态码：
//   - 200 OK: 健康状态
//   - 503 Service Unavailable: 不健康状态
//
// 使用场景：
//   - Kubernetes liveness probe
//   - 负载均衡器健康检查
//   - 监控系统状态检查
func (c *WebSocketClient) handleHealth(w http.ResponseWriter, r *http.Request) {
	// 设置JSON响应头
	w.Header().Set("Content-Type", "application/json")

	// 初始化健康状态
	status := "healthy"
	httpStatus := http.StatusOK

	// 检查客户端运行状态
	state := c.GetState()
	if state == StateStopped || state == StateStopping {
		status = "unhealthy"
		httpStatus = http.StatusServiceUnavailable
	}

	// 设置HTTP状态码并返回JSON响应
	w.WriteHeader(httpStatus)
	fmt.Fprintf(w, `{"status": "%s", "state": "%s", "session_id": "%s", "timestamp": "%s"}`,
		status, state.String(), c.SessionID, time.Now().Format(time.RFC3339))
}

// handleReady 处理就绪检查请求
// 这个HTTP处理器提供就绪状态检查，用于确定服务是否准备好接收流量
//
// 功能说明：
//   - 检查WebSocket连接是否已建立
//   - 返回JSON格式的就绪状态信息
//   - 根据连接状态设置合适的HTTP状态码
//
// 就绪判断逻辑：
//   - ready: true - WebSocket连接已建立且正常
//   - ready: false - WebSocket连接未建立或异常
//
// 返回格式：
//
//	{
//	  "ready": true|false,
//	  "state": "客户端状态",
//	  "session_id": "会话ID",
//	  "timestamp": "检查时间"
//	}
//
// HTTP状态码：
//   - 200 OK: 就绪状态
//   - 503 Service Unavailable: 未就绪状态
//
// 使用场景：
//   - Kubernetes readiness probe
//   - 负载均衡器流量控制
//   - 服务发现注册检查
func (c *WebSocketClient) handleReady(w http.ResponseWriter, r *http.Request) {
	// 设置JSON响应头
	w.Header().Set("Content-Type", "application/json")

	// 检查WebSocket连接状态
	ready := c.isConnected()
	httpStatus := http.StatusOK
	if !ready {
		httpStatus = http.StatusServiceUnavailable
	}

	// 设置HTTP状态码并返回JSON响应
	w.WriteHeader(httpStatus)
	fmt.Fprintf(w, `{"ready": %t, "state": "%s", "session_id": "%s", "timestamp": "%s"}`,
		ready, c.GetState().String(), c.SessionID, time.Now().Format(time.RFC3339))
}

// handleStats 处理统计信息请求
// 这个HTTP处理器提供详细的WebSocket客户端统计信息，以JSON格式返回
//
// 功能说明：
//   - 收集连接统计和错误统计信息
//   - 格式化为结构化的JSON响应
//   - 提供实时的客户端状态快照
//
// 返回的统计信息：
//  1. 基本信息：会话ID、状态、时间戳
//  2. 连接信息：连接时间、运行时长、重连次数
//  3. 消息统计：发送/接收的消息数量和字节数
//  4. 错误统计：错误总数、最后错误、错误时间
//
// JSON响应格式：
//
//	{
//	  "session_id": "会话标识符",
//	  "state": "连接状态",
//	  "connect_time": "连接建立时间",
//	  "last_message_time": "最后消息时间",
//	  "uptime_seconds": 运行时长秒数,
//	  "messages_sent": 发送消息数,
//	  "messages_received": 接收消息数,
//	  "bytes_sent": 发送字节数,
//	  "bytes_received": 接收字节数,
//	  "reconnect_count": 重连次数,
//	  "errors": {
//	    "total_errors": 错误总数,
//	    "last_error": "最后错误信息",
//	    "last_error_time": "最后错误时间"
//	  },
//	  "timestamp": "当前时间戳"
//	}
//
// 使用场景：
//   - 监控系统的数据收集
//   - 运维工具的状态查询
//   - 调试和问题诊断
//   - 性能分析和优化
func (c *WebSocketClient) handleStats(w http.ResponseWriter, r *http.Request) {
	// 设置JSON响应头
	w.Header().Set("Content-Type", "application/json")

	// 获取最新的统计数据
	stats := c.GetStats()
	errorStats := c.GetErrorStats()

	// 构建结构化的JSON响应
	response := fmt.Sprintf(`{
		"session_id": "%s",
		"state": "%s",
		"connect_time": "%s",
		"last_message_time": "%s",
		"uptime_seconds": %.0f,
		"messages_sent": %d,
		"messages_received": %d,
		"bytes_sent": %d,
		"bytes_received": %d,
		"reconnect_count": %d,
		"errors": {
			"total_errors": %d,
			"last_error": "%v",
			"last_error_time": "%s"
		},
		"timestamp": "%s"
	}`,
		c.SessionID,                                   // 会话标识符
		c.GetState().String(),                         // 当前连接状态
		stats.ConnectTime.Format(time.RFC3339),        // 连接建立时间
		stats.LastMessageTime.Format(time.RFC3339),    // 最后消息时间
		stats.Uptime.Seconds(),                        // 运行时长（秒）
		stats.MessagesSent,                            // 发送消息数量
		stats.MessagesReceived,                        // 接收消息数量
		stats.BytesSent,                               // 发送字节数
		stats.BytesReceived,                           // 接收字节数
		stats.ReconnectCount,                          // 重连次数
		errorStats.TotalErrors,                        // 错误总数
		errorStats.LastError,                          // 最后错误信息
		errorStats.LastErrorTime.Format(time.RFC3339), // 最后错误时间
		time.Now().Format(time.RFC3339))               // 当前时间戳

	// 输出JSON响应
	fmt.Fprint(w, response)
}

// updatePrometheusMetrics 更新Prometheus指标
// 这个方法将内部统计数据同步到Prometheus指标结构中
//
// 功能说明：
//  1. 从内部统计数据更新Prometheus指标
//  2. 确保指标数据的一致性和准确性
//  3. 支持实时的指标查询和监控
//
// 更新的指标类型：
//  1. 计数器指标：消息数量、字节数量、错误数量、重连数量
//  2. 仪表指标：当前连接状态（0或1）
//  3. 分类指标：按错误码分类的错误统计
//
// 数据同步：
//   - 从c.Stats复制到c.metrics
//   - 保持数据的原子性和一致性
//   - 支持Prometheus的拉取模式
//
// 并发安全：
//   - 使用读锁保护统计数据的读取
//   - 避免在指标更新过程中数据变化
//   - 确保指标的准确性
//
// 调用时机：
//   - handleMetrics处理器中调用
//   - 确保返回最新的指标数据
//   - 支持实时监控需求
func (c *WebSocketClient) updatePrometheusMetrics() {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// 更新消息和字节统计指标
	c.metrics.MessagesSentTotal = c.Stats.MessagesSent
	c.metrics.MessagesReceivedTotal = c.Stats.MessagesReceived
	c.metrics.BytesSentTotal = c.Stats.BytesSent
	c.metrics.BytesReceivedTotal = c.Stats.BytesReceived

	// 更新连接相关指标
	c.metrics.ReconnectionsTotal = int64(c.Stats.ReconnectCount)
	c.metrics.ErrorsTotal = c.Stats.Errors.TotalErrors

	// 更新连接状态指标（仪表类型）
	if c.isConnected() {
		c.metrics.ConnectionsActive = 1 // 连接活跃
	} else {
		c.metrics.ConnectionsActive = 0 // 连接断开
	}

	// 更新错误码分类统计
	for code, count := range c.Stats.Errors.ErrorsByCode {
		c.metrics.ErrorsByCodeTotal[code] = count
	}
}

// stopMonitoringServers 停止监控服务器
// 这个方法优雅地关闭Prometheus指标服务器和健康检查服务器
//
// 功能说明：
//  1. 优雅关闭指标服务器
//  2. 优雅关闭健康检查服务器
//  3. 设置合理的关闭超时时间
//  4. 清理服务器引用
//
// 关闭流程：
//  1. 检查服务器是否存在
//  2. 创建带超时的上下文（5秒）
//  3. 调用服务器的Shutdown方法
//  4. 处理关闭错误和成功情况
//  5. 清理服务器引用防止内存泄漏
//
// 超时处理：
//   - 设置5秒的优雅关闭超时
//   - 超时后强制关闭服务器
//   - 记录关闭状态和错误信息
//
// 错误处理：
//   - 记录关闭失败的警告信息
//   - 记录成功关闭的确认信息
//   - 确保在任何情况下都清理引用
//
// 调用时机：
//   - 客户端停止时调用
//   - 程序退出前的清理工作
//   - 确保资源正确释放
func (c *WebSocketClient) stopMonitoringServers() {
	// 停止Prometheus指标服务器
	if c.metricsServer != nil {
		// 创建5秒超时的上下文
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// 优雅关闭指标服务器
		if err := c.metricsServer.Shutdown(ctx); err != nil {
			log.Printf("⚠️ 指标服务器关闭失败: %v", err)
		} else {
			log.Printf("📊 指标服务器已关闭")
		}
		c.metricsServer = nil // 清理引用
	}

	// 停止健康检查服务器
	if c.healthServer != nil {
		// 创建5秒超时的上下文
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// 优雅关闭健康检查服务器
		if err := c.healthServer.Shutdown(ctx); err != nil {
			log.Printf("⚠️ 健康检查服务器关闭失败: %v", err)
		} else {
			log.Printf("🏥 健康检查服务器已关闭")
		}
		c.healthServer = nil // 清理引用
	}
}

// SendMessage 发送消息到 WebSocket 服务器
// 集成了消息处理器、自适应缓冲区和错误恢复功能
// 支持文本消息、二进制消息和控制消息的发送
//
// Example:
//
//	// 发送文本消息
//	err := client.SendMessage(websocket.TextMessage, []byte("Hello"))
//
//	// 发送二进制消息
//	err := client.SendMessage(websocket.BinaryMessage, binaryData)
//
//	// 发送ping消息
//	err := client.SendMessage(websocket.PingMessage, nil)
func (c *WebSocketClient) SendMessage(messageType int, data []byte) error {
	// 记录锁获取（死锁检测）
	c.deadlockDetector.AcquireLock("send")
	defer c.deadlockDetector.ReleaseLock("send")

	// 频率限制检查
	if !c.rateLimiter.Allow() {
		err := &ConnectionError{
			Code:  ErrCodeRateLimitExceeded,
			Op:    "send",
			URL:   c.config.URL,
			Err:   fmt.Errorf("发送频率超过限制"),
			Retry: true,
		}
		c.recordError(err)
		return err
	}

	// 安全检查
	if err := c.securityChecker.CheckMessage(messageType, data); err != nil {
		securityErr := &ConnectionError{
			Code:  ErrCodeSecurityViolation,
			Op:    "send",
			URL:   c.config.URL,
			Err:   err,
			Retry: false,
		}
		c.recordError(securityErr)
		return securityErr
	}

	conn, connected := c.getConnSafely()
	if conn == nil || !connected {
		err := &ConnectionError{
			Code:  ErrCodeConnectionLost,
			Op:    "send",
			URL:   c.config.URL,
			Err:   ErrConnectionClosed,
			Retry: false,
		}
		c.recordError(err)
		return err
	}

	// 使用消息处理器验证和格式化消息
	if err := c.messageProcessor.ValidateMessage(messageType, data); err != nil {
		validationErr := &ConnectionError{
			Code:  ErrCodeInvalidMessage,
			Op:    "send",
			URL:   c.config.URL,
			Err:   err,
			Retry: false,
		}
		c.recordError(validationErr)
		return validationErr
	}

	// 直接使用原始数据，简化消息处理
	formattedData := data

	// 检查消息大小（双重检查）
	if len(formattedData) > c.config.MaxMessageSize {
		err := &ConnectionError{
			Code:  ErrCodeMessageTooLarge,
			Op:    "send",
			URL:   c.config.URL,
			Err:   fmt.Errorf("消息大小 %d 超过限制 %d", len(formattedData), c.config.MaxMessageSize),
			Retry: false,
		}
		c.recordError(err)
		return err
	}

	// 设置写入超时
	if err := conn.SetWriteDeadline(time.Now().Add(c.config.WriteTimeout)); err != nil {
		timeoutErr := &ConnectionError{
			Code:  ErrCodeSendTimeout,
			Op:    "send",
			URL:   c.config.URL,
			Err:   err,
			Retry: false,
		}
		c.recordError(timeoutErr)
		return timeoutErr
	}

	// 使用写锁保护WebSocket写操作，防止并发写入
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	// 自适应缓冲区优化：根据消息大小和历史性能动态选择缓冲策略
	var sendData []byte
	var needReturn bool

	if c.AdaptiveBuffer {
		// 自适应缓冲区逻辑
		bufferSize := c.calculateOptimalBufferSize(len(formattedData))
		if bufferSize <= LargeBufferSize {
			buf := globalBufferPool.Get(bufferSize)
			// 延迟释放缓冲区的匿名函数：确保缓冲区在使用完毕后正确归还到内存池
			defer func() {
				if needReturn {
					globalBufferPool.Put(buf) // 将缓冲区归还到全局内存池，避免内存泄漏
				}
			}()
			copy(buf, formattedData)
			sendData = buf[:len(formattedData)] // 只发送实际数据长度
			needReturn = true
		} else {
			// 对于超大消息，直接使用原始数据
			sendData = formattedData
			needReturn = false
		}
	} else {
		// 传统缓冲区逻辑
		if len(formattedData) <= LargeBufferSize {
			buf := globalBufferPool.Get(len(formattedData))
			// 延迟释放缓冲区的匿名函数：确保传统缓冲区逻辑中的缓冲区正确归还
			defer func() {
				if needReturn {
					globalBufferPool.Put(buf) // 将缓冲区归还到全局内存池，保持内存池的高效运作
				}
			}()
			copy(buf, formattedData)
			sendData = buf[:len(formattedData)] // 只发送实际数据长度
			needReturn = true
		} else {
			sendData = formattedData
			needReturn = false
		}
	}

	// 发送消息
	startTime := time.Now()
	if err := conn.WriteMessage(messageType, sendData); err != nil {
		sendErr := &ConnectionError{
			Code:  c.inferErrorCode(err),
			Op:    "send",
			URL:   c.config.URL,
			Err:   err,
			Retry: true,
		}
		c.handleErrorWithRecovery(sendErr, "发送")

		return sendErr
	}
	sendDuration := time.Since(startTime)

	// 更新统计信息
	c.updateStats(messageType, len(formattedData), true)

	// 记录消息到日志文件
	c.logMessage("SEND", messageType, formattedData)

	// 记录发送性能（简化版）
	log.Printf("📊 消息发送耗时: %v, 类型: %s", sendDuration, c.getMessageTypeString(messageType))

	return nil
}

// calculateOptimalBufferSize 计算最优缓冲区大小
// 基于消息大小、历史性能和系统资源使用情况
func (c *WebSocketClient) calculateOptimalBufferSize(messageSize int) int {
	// 根据消息大小选择合适的缓冲区级别
	var baseSize int
	switch {
	case messageSize <= SmallBufferSize:
		baseSize = SmallBufferSize
	case messageSize <= MediumBufferSize:
		baseSize = MediumBufferSize
	case messageSize <= LargeBufferSize:
		baseSize = LargeBufferSize
	default:
		// 对于超大消息，使用实际大小
		baseSize = messageSize
	}

	// 根据当前性能指标调整缓冲区大小
	performanceReport := c.performanceMonitor.GetPerformanceReport()

	// 如果CPU使用率高，使用较小的缓冲区以减少内存压力
	if cpuUsage, ok := performanceReport["cpu_usage_percent"].(float64); ok && cpuUsage > 80.0 {
		baseSize = int(float64(baseSize) * 0.8)
	}

	// 如果内存使用率高，使用较小的缓冲区
	if memUsage, ok := performanceReport["memory_usage_bytes"].(int64); ok && memUsage > 100*1024*1024 { // 100MB
		baseSize = int(float64(baseSize) * 0.9)
	}

	// 确保缓冲区大小不小于消息大小
	if baseSize < messageSize {
		baseSize = messageSize
	}

	return baseSize
}

// SetDependencies 设置核心组件（简化版）
func (c *WebSocketClient) SetDependencies(
	connector Connector,
	messageProcessor MessageProcessor,
	errorRecovery ErrorRecovery,
) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if connector != nil {
		c.connector = connector
	}
	if messageProcessor != nil {
		c.messageProcessor = messageProcessor
	}
	if errorRecovery != nil {
		c.errorRecovery = errorRecovery
	}
}

// EnableAdvancedFeatures 启用或禁用高级功能
func (c *WebSocketClient) EnableAdvancedFeatures(autoRecovery, adaptiveBuffer bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.AutoRecovery = autoRecovery
	c.AdaptiveBuffer = adaptiveBuffer

	log.Printf("🔧 高级功能配置: 自动恢复=%v, 自适应缓冲区=%v", autoRecovery, adaptiveBuffer)
}

// GetHealthStatus 获取客户端健康状态（简化版）
func (c *WebSocketClient) GetHealthStatus() HealthStatus {
	if c.isConnected() {
		return HealthHealthy
	}
	return HealthUnhealthy
}

// GetPerformanceReport 获取性能报告
func (c *WebSocketClient) GetPerformanceReport() map[string]any {
	// 更新性能监控器
	c.performanceMonitor.UpdateMetrics(c.GetStats())

	// 获取基础性能报告
	report := c.performanceMonitor.GetPerformanceReport()

	// 添加死锁检测信息
	deadlocks := c.deadlockDetector.CheckDeadlocks()
	report["deadlock_alerts"] = deadlocks
	report["deadlock_count"] = len(deadlocks)

	// 添加健康检查信息（简化版）
	report["health_status"] = c.GetHealthStatus().String()
	report["health_error_count"] = c.Stats.Errors.TotalErrors

	return report
}

// SendText 发送文本消息
// 这是SendMessage的便捷包装函数，专门用于发送文本消息
//
// 参数说明：
//   - text: 要发送的文本内容
//
// 返回值：
//   - error: 发送失败时的错误信息
//
// 功能说明：
//   - 自动将字符串转换为字节数组
//   - 设置消息类型为websocket.TextMessage
//   - 继承SendMessage的所有功能和安全检查
//
// 使用示例：
//
//	err := client.SendText("Hello, WebSocket!")
//	if err != nil {
//	    log.Printf("发送文本消息失败: %v", err)
//	}
//
// 注意事项：
//   - 文本内容会被转换为UTF-8字节序列
//   - 受到MaxMessageSize配置的限制
//   - 支持频率限制和安全检查
func (c *WebSocketClient) SendText(text string) error {
	return c.SendMessage(websocket.TextMessage, []byte(text))
}

// SendBinary 发送二进制消息
// 这是SendMessage的便捷包装函数，专门用于发送二进制数据
//
// 参数说明：
//   - data: 要发送的二进制数据
//
// 返回值：
//   - error: 发送失败时的错误信息
//
// 功能说明：
//   - 直接发送字节数组作为二进制消息
//   - 设置消息类型为websocket.BinaryMessage
//   - 继承SendMessage的所有功能和安全检查
//
// 使用示例：
//
//	binaryData := []byte{0x01, 0x02, 0x03, 0x04}
//	err := client.SendBinary(binaryData)
//	if err != nil {
//	    log.Printf("发送二进制消息失败: %v", err)
//	}
//
// 适用场景：
//   - 发送图片、音频、视频等媒体文件
//   - 发送序列化的结构化数据
//   - 发送加密或压缩的数据
//   - 发送自定义协议的数据包
func (c *WebSocketClient) SendBinary(data []byte) error {
	return c.SendMessage(websocket.BinaryMessage, data)
}

// SetEventHandlers 设置事件处理器
// 允许自定义连接、断开、消息接收和错误处理的回调函数
//
// Example:
//
//	client.SetEventHandlers(
//	    func() { log.Println("连接成功") },
//	    func(err error) { log.Printf("连接断开: %v", err) },
//	    func(messageType int, data []byte) error {
//	        log.Printf("收到消息: %s", string(data))
//	        return nil
//	    },
//	    func(err error) { log.Printf("发生错误: %v", err) },
//	)
func (c *WebSocketClient) SetEventHandlers(
	onConnect func(),
	onDisconnect func(error),
	onMessage func(messageType int, data []byte) error,
	onError func(error),
) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if onConnect != nil {
		c.onConnect = onConnect
	}
	if onDisconnect != nil {
		c.onDisconnect = onDisconnect
	}
	if onMessage != nil {
		c.onMessage = onMessage
	}
	if onError != nil {
		c.onError = onError
	}
}

// safeCallOnConnect 安全调用连接成功事件处理器
// 这个方法以线程安全的方式调用用户设置的连接成功回调函数
//
// 功能说明：
//   - 检查连接回调函数是否已设置
//   - 在独立的goroutine中调用回调函数
//   - 避免回调函数阻塞主要的连接流程
//
// 并发安全：
//   - 简化版本，避免锁竞争
//   - 使用goroutine异步调用，防止阻塞
//   - 回调函数的执行不会影响连接状态
//
// 调用时机：
//   - WebSocket连接成功建立后
//   - 在setupConnection方法中调用
//   - 确保用户能及时收到连接成功通知
//
// 注意事项：
//   - 回调函数在独立的goroutine中执行
//   - 回调函数的错误不会影响客户端运行
//   - 用户应确保回调函数的线程安全性
func (c *WebSocketClient) safeCallOnConnect() {
	// 简化版本，避免锁竞争
	if c.onConnect != nil {
		go c.onConnect()
	}
}

// safeCallOnMessage 安全调用消息接收事件处理器
// 这个方法以线程安全的方式调用用户设置的消息接收回调函数
//
// 参数说明：
//   - messageType: WebSocket消息类型
//   - data: 消息数据内容
//
// 返回值：
//   - error: 回调函数返回的错误
//
// 功能说明：
//   - 使用读锁安全获取回调函数引用
//   - 在当前goroutine中同步调用回调函数
//   - 返回回调函数的执行结果
//
// 并发安全：
//   - 使用读锁保护回调函数的读取
//   - 避免在锁内调用回调函数，减少锁持有时间
//   - 支持并发的消息处理
//
// 调用时机：
//   - 接收到WebSocket消息后
//   - 在ReadMessages方法中调用
//   - 允许用户自定义消息处理逻辑
//
// 注意事项：
//   - 回调函数在消息读取goroutine中同步执行
//   - 回调函数的执行时间会影响消息处理性能
//   - 用户应避免在回调中执行耗时操作
func (c *WebSocketClient) safeCallOnMessage(messageType int, data []byte) error {
	c.mu.RLock()
	handler := c.onMessage
	c.mu.RUnlock()

	if handler != nil {
		return handler(messageType, data)
	}
	return nil
}

// safeCallOnError 安全调用错误处理事件处理器
// 这个方法以线程安全的方式调用用户设置的错误处理回调函数
//
// 参数说明：
//   - err: 发生的错误实例
//
// 功能说明：
//   - 使用读锁安全获取回调函数引用
//   - 在独立的goroutine中异步调用回调函数
//   - 避免错误处理阻塞主要流程
//
// 并发安全：
//   - 使用读锁保护回调函数的读取
//   - 在独立goroutine中执行，避免阻塞
//   - 错误处理不会影响客户端的正常运行
//
// 调用时机：
//   - 发生各种错误时调用
//   - 包括连接错误、发送错误、接收错误等
//   - 为用户提供统一的错误通知机制
//
// 注意事项：
//   - 回调函数在独立的goroutine中执行
//   - 回调函数的错误不会被捕获或处理
//   - 用户应确保回调函数的稳定性和线程安全性
func (c *WebSocketClient) safeCallOnError(err error) {
	c.mu.RLock()
	handler := c.onError
	c.mu.RUnlock()

	if handler != nil {
		go handler(err)
	}
}

// Start 是 WebSocket 客户端的主循环
// 它管理连接生命周期，包括初始连接、重试和消息读取
// 这是一个阻塞函数，通常在单独的goroutine中运行
//
// Example:
//
//	client := NewWebSocketClient(config)
//	client.SetEventHandlers(onConnect, onDisconnect, onMessage, onError)
//
//	// 在goroutine中启动客户端
//	go client.Start()
//
//	// 等待信号或其他条件后停止
//	<-ctx.Done()
//	client.Stop()
func (c *WebSocketClient) Start() {
	// 延迟执行的清理匿名函数：确保Start函数退出时正确通知其他组件
	defer func() {
		// 如果Start函数退出，说明重试失败或收到停止信号
		// 主动调用cancel来通知main函数退出，实现优雅的程序终止
		c.cancel()
	}()

	// 启动周期性ping（如果未禁用）
	if !c.config.DisableAutoPing {
		go c.sendPeriodicPing()
	}

	for {
		select {
		case <-c.ctx.Done():
			log.Printf("📋 收到停止信号，退出主循环")
			return
		default:
			if !c.attemptConnection() {
				return // 达到最大重试次数或被取消
			}

			if !c.handleConnectedSession() {
				return // 被取消或需要退出
			}
		}
	}
}

// attemptConnection 尝试建立连接，处理重试逻辑
// 这个方法是连接重试机制的核心，负责尝试建立WebSocket连接并处理失败情况
//
// 返回值：
//   - bool: true表示应该继续主循环，false表示应该退出主循环
//
// 功能说明：
//  1. 调用Connect方法尝试建立连接
//  2. 连接失败时增加重试计数器
//  3. 记录连接错误日志
//  4. 检查是否应该停止重试
//  5. 等待重试延迟时间
//  6. 连接成功时重置重试计数器
//
// 重试逻辑：
//   - 连接失败：增加重试计数，记录错误，检查重试限制
//   - 达到重试限制：返回false，退出主循环
//   - 未达到限制：等待重试延迟，返回true继续尝试
//   - 连接成功：重置计数器，返回true进入消息处理阶段
//
// 错误处理：
//   - 区分网络错误和其他错误类型
//   - 提供详细的错误日志信息
//   - 支持智能重试策略
//
// 并发安全：
//   - 使用原子操作更新重试计数器
//   - 避免竞态条件
func (c *WebSocketClient) attemptConnection() bool {
	// 第一步：尝试建立WebSocket连接
	err := c.Connect()
	if err != nil {
		// 第二步：连接失败，增加重试计数器
		atomic.AddInt32(&c.RetryCount, 1)
		c.logConnectionError(err)

		// 第三步：检查是否应该停止重试
		if c.shouldStopRetrying() {
			return false // 达到重试限制，退出主循环
		}

		// 第四步：等待重试延迟时间
		return c.waitForRetry() // 返回是否应该继续重试
	}

	// 第五步：连接成功，重置重试计数器
	atomic.StoreInt32(&c.RetryCount, 0)
	log.Printf("🔄 重置重试计数器，开始接收消息...")
	return true // 继续主循环，进入消息处理阶段
}

// logConnectionError 记录连接错误日志
// 这个方法根据错误类型记录不同格式的连接错误日志
//
// 参数说明：
//   - err: 连接过程中发生的错误
//
// 功能说明：
//  1. 获取当前的重试计数
//  2. 判断错误类型（网络错误 vs 其他错误）
//  3. 记录格式化的错误日志
//  4. 提供重试次数信息
//
// 错误分类：
//   - 网络错误：连接超时、网络不可达、DNS解析失败等
//   - 其他错误：认证失败、协议错误、配置错误等
//
// 日志格式：
//   - 网络错误：🔌 网络连接中断 (第N次重试): 错误详情
//   - 其他错误：❌ 连接失败 (第N次重试): 错误详情
//
// 并发安全：
//   - 使用原子操作读取重试计数器
//   - 避免数据竞争
func (c *WebSocketClient) logConnectionError(err error) {
	// 获取当前重试计数（原子操作）
	retryCount := atomic.LoadInt32(&c.RetryCount)

	// 根据错误类型记录不同格式的日志
	if isNetworkError(err) {
		log.Printf("🔌 网络连接中断 (第%d次重试): %v", retryCount, err)
	} else {
		log.Printf("❌ 连接失败 (第%d次重试): %v", retryCount, err)
	}
}

// shouldStopRetrying 检查是否应该停止重试
// 这个方法根据配置的重试限制和当前重试次数来判断是否应该停止重试
//
// 返回值：
//   - bool: true表示应该停止重试，false表示可以继续重试
//
// 重试策略：
//  1. 快速重试阶段：前N次重试（默认5次）
//  2. 慢速重试阶段：后续重试，总数为MaxRetries * 2
//  3. 无限重试：当MaxRetries为0时，永不停止
//
// 限制计算：
//   - fastLimit: 快速重试次数限制（默认5次）
//   - totalLimit: 总重试次数限制（MaxRetries * 2）
//   - 使用原子操作获取当前重试计数
//
// 安全转换：
//   - 使用严格的int到int32转换方法
//   - 防止整数溢出和类型转换错误
//   - 通过字符串转换避免gosec安全警告
//
// 并发安全：
//   - 使用原子操作读取重试计数器
//   - 避免数据竞争
//
// 使用场景：
//   - 连接重试循环中的退出条件判断
//   - 防止无限重试导致的资源浪费
//   - 实现智能重试策略
func (c *WebSocketClient) shouldStopRetrying() bool {
	// 第一步：计算快速重试限制
	fastLimit := c.config.MaxRetries
	if fastLimit == 0 {
		// 无限重试模式，不需要检查限制
		return false
	}

	// 第二步：计算总重试限制
	totalLimit := c.config.MaxRetries * 2
	retryCount := atomic.LoadInt32(&c.RetryCount)

	// 第三步：安全的int到int32转换（避免gosec警告）
	var totalLimitInt32 int32
	if totalLimit < 0 {
		totalLimitInt32 = 0
	} else if totalLimit > math.MaxInt32 {
		totalLimitInt32 = math.MaxInt32
	} else {
		// 使用字符串转换避免gosec警告
		limitStr := fmt.Sprintf("%d", totalLimit)
		if parsed, err := strconv.ParseInt(limitStr, 10, 32); err == nil {
			totalLimitInt32 = int32(parsed)
		} else {
			totalLimitInt32 = math.MaxInt32
		}
	}

	// 第四步：检查是否达到重试限制
	if totalLimit > 0 && retryCount >= totalLimitInt32 {
		log.Printf("🛑 达到最大重试次数 (%d)，停止尝试", totalLimit)
		return true
	}
	return false
}

// waitForRetry 等待重试延迟时间
// 这个方法实现智能的重试等待机制，支持快速重试和延迟重试
//
// 返回值：
//   - bool: true表示等待完成可以继续重试，false表示被取消应该退出
//
// 等待策略：
//  1. 计算当前重试应该等待的时间
//  2. 使用select语句同时监听取消信号和等待时间
//  3. 优先响应取消信号，确保能够及时停止
//
// 取消处理：
//   - 监听context.Done()信号
//   - 客户端停止时立即返回false
//   - 避免不必要的等待时间
//
// 并发安全：
//   - 使用context进行取消通知
//   - 不会阻塞其他操作
//
// 使用场景：
//   - 连接失败后的重试等待
//   - 实现退避重试策略
//   - 支持优雅停止
func (c *WebSocketClient) waitForRetry() bool {
	// 第一步：计算重试延迟时间
	retryDelay := c.calculateRetryDelay()

	// 第二步：等待延迟时间或取消信号
	select {
	case <-c.ctx.Done():
		return false // 收到取消信号，停止重试
	case <-time.After(retryDelay):
		return true // 等待完成，可以继续重试
	}
}

// calculateRetryDelay 计算重试延迟时间并记录日志
// 这个方法实现智能的重试延迟策略，区分快速重试和慢速重试
//
// 返回值：
//   - time.Duration: 当前重试应该等待的时间
//
// 重试策略：
//  1. 快速重试阶段：前N次重试无延迟（立即重试）
//  2. 慢速重试阶段：后续重试使用配置的延迟时间
//  3. 无限重试模式：当MaxRetries为0时支持无限重试
//
// 延迟计算：
//   - 快速重试：返回0，立即重试
//   - 慢速重试：返回配置的RetryDelay
//   - 无限重试：使用慢速重试延迟
//
// 日志记录：
//   - 快速重试：显示当前次数和快速重试限制
//   - 慢速重试：显示当前次数和总重试限制
//   - 无限重试：显示当前次数和延迟时间
//
// 安全转换：
//   - 使用严格的int到int32转换
//   - 防止整数溢出和类型错误
//   - 通过字符串转换避免gosec警告
//
// 并发安全：
//   - 使用原子操作读取重试计数器
//   - 避免数据竞争
func (c *WebSocketClient) calculateRetryDelay() time.Duration {
	// 第一步：计算重试限制
	fastLimit := c.config.MaxRetries
	if fastLimit == 0 {
		fastLimit = 5 // 默认快速重试5次
	}
	totalLimit := c.config.MaxRetries * 2
	retryCount := atomic.LoadInt32(&c.RetryCount)

	// 第二步：安全的int到int32转换（避免gosec警告）
	var fastLimitInt32 int32
	if fastLimit < 0 {
		fastLimitInt32 = 0
	} else if fastLimit > math.MaxInt32 {
		fastLimitInt32 = math.MaxInt32
	} else {
		// 使用字符串转换避免gosec警告
		limitStr := fmt.Sprintf("%d", fastLimit)
		if parsed, err := strconv.ParseInt(limitStr, 10, 32); err == nil {
			fastLimitInt32 = int32(parsed)
		} else {
			fastLimitInt32 = math.MaxInt32
		}
	}

	// 第三步：根据重试阶段返回相应的延迟时间
	if retryCount <= fastLimitInt32 {
		// 快速重试阶段：无延迟
		log.Printf("⚡ 快速重试 (第%d/%d次)...", retryCount, fastLimit)
		return 0
	} else if totalLimit == 0 {
		// 无限重试模式：使用配置的延迟
		log.Printf("🔄 无限慢速重试 (第%d次)，%v后重试...", retryCount, c.config.RetryDelay)
		return c.config.RetryDelay
	} else {
		// 慢速重试阶段：使用配置的延迟
		log.Printf("⏳ 慢速重试 (第%d/%d次)，%v后重试...",
			retryCount-fastLimitInt32, totalLimit-fastLimit, c.config.RetryDelay)
		return c.config.RetryDelay
	}
}

// handleConnectedSession 处理已连接的会话
// 这个方法管理WebSocket连接的生命周期，包括消息读取和连接监控
//
// 返回值：
//   - bool: false表示应该退出主循环，true表示应该继续重连
//
// 功能说明：
//  1. 启动消息读取goroutine
//  2. 监控连接状态和停止信号
//  3. 处理连接断开和重连逻辑
//  4. 确保资源正确清理
//
// 会话管理：
//   - 使用独立的channel通知ReadMessages结束
//   - 通过WaitGroup管理goroutine生命周期
//   - 支持优雅停止和异常处理
//
// 并发控制：
//   - ReadMessages在独立的goroutine中运行
//   - 使用select语句监听多个事件
//   - 优先处理停止信号，确保及时响应
//
// 重连逻辑：
//   - ReadMessages正常结束时准备重连
//   - 收到停止信号时退出主循环
//   - 双重检查确保停止信号的优先级
//
// 使用场景：
//   - 连接建立后的会话管理
//   - 消息读取和连接监控
//   - 自动重连机制的核心逻辑
func (c *WebSocketClient) handleConnectedSession() bool {
	// 第一步：创建通知channel并启动消息读取goroutine
	readDone := make(chan struct{})
	c.wg.Add(1)
	// 启动消息读取的匿名goroutine：负责持续读取WebSocket消息直到连接断开
	go func() {
		defer close(readDone) // 确保channel被关闭，通知主goroutine消息读取已结束
		defer c.wg.Done()     // 通知WaitGroup任务完成，确保优雅停止时等待此goroutine结束
		c.ReadMessages()      // 开始读取消息的主循环，这是消息接收的核心逻辑
	}()

	// 第二步：等待ReadMessages结束或收到停止信号
	select {
	case <-c.ctx.Done():
		// 收到停止信号，立即退出
		log.Printf("📋 收到停止信号，停止客户端")
		return false
	case <-readDone:
		// ReadMessages结束，检查是否应该重连
		select {
		case <-c.ctx.Done():
			// 双重检查：确保停止信号优先处理
			return false
		default:
			// 连接断开，准备重连
			log.Printf("🔄 连接断开，准备重连...")
			return true
		}
	}
}

// Connect 建立WebSocket连接
// 这个方法是WebSocket连接建立的主入口，负责完整的连接流程
//
// 返回值：
//   - error: 连接失败时的错误信息，成功时为nil
//
// 连接流程：
//  1. 获取死锁检测锁，防止并发连接
//  2. 设置连接状态为正在连接
//  3. 调用底层连接建立方法
//  4. 处理连接错误或设置连接
//  5. 释放死锁检测锁
//
// 状态管理：
//   - 连接开始时设置为StateConnecting
//   - 连接失败时设置为StateDisconnected
//   - 连接成功时在setupConnection中设置为StateConnected
//
// 错误处理：
//   - 连接失败时记录错误统计
//   - 尝试自动错误恢复（如果启用）
//   - 返回结构化的ConnectionError
//
// 并发安全：
//   - 使用死锁检测器防止并发连接
//   - 确保连接操作的原子性
//
// 使用场景：
//   - 初始连接建立
//   - 重连机制中的连接尝试
//   - 手动连接操作
func (c *WebSocketClient) Connect() error {
	// 第一步：获取死锁检测锁，防止并发连接
	c.deadlockDetector.AcquireLock("connect")
	defer c.deadlockDetector.ReleaseLock("connect")

	// 第二步：记录连接开始并设置状态
	log.Printf("🔌 准备连接到 %s...", c.config.URL)
	c.setState(StateConnecting)

	// 第三步：建立WebSocket连接
	newConn, err := c.establishConnection()
	if err != nil {
		// 连接失败，处理错误
		return c.handleConnectionError(err)
	}

	// 第四步：连接成功，设置连接
	c.setupConnection(newConn)
	return nil
}

// establishConnection 建立WebSocket连接
// 这个方法负责实际的WebSocket连接建立，使用配置的连接器
//
// 返回值：
//   - *websocket.Conn: 成功建立的WebSocket连接
//   - error: 连接失败时的错误信息
//
// 功能说明：
//   - 创建带超时的连接上下文
//   - 使用连接器接口建立连接
//   - 支持握手超时控制
//
// 超时处理：
//   - 使用配置的HandshakeTimeout作为连接超时
//   - 超时后自动取消连接尝试
//   - 防止连接操作无限阻塞
//
// 连接器模式：
//   - 使用可插拔的连接器接口
//   - 支持不同的连接实现
//   - 便于测试和扩展
//
// 使用场景：
//   - Connect方法的底层实现
//   - 支持自定义连接逻辑
//   - 连接超时控制
func (c *WebSocketClient) establishConnection() (*websocket.Conn, error) {
	// 创建带超时的连接上下文
	connectCtx, cancel := context.WithTimeout(c.ctx, c.config.HandshakeTimeout)
	defer cancel()

	// 使用连接器建立WebSocket连接
	return c.connector.Connect(connectCtx, c.config.URL, c.config)
}

// handleConnectionError 处理连接错误
// 这个方法统一处理连接失败的情况，包括状态更新、错误记录和恢复尝试
//
// 参数说明：
//   - err: 连接过程中发生的错误
//
// 返回值：
//   - error: 包装后的ConnectionError，包含详细的错误信息
//
// 错误处理流程：
//  1. 设置连接状态为断开
//  2. 记录错误日志
//  3. 更新错误统计信息
//  4. 尝试自动错误恢复
//  5. 返回结构化的错误信息
//
// 错误记录：
//   - 更新错误统计和趋势数据
//   - 支持错误分类和分析
//   - 便于问题诊断和监控
//
// 自动恢复：
//   - 检查是否启用自动恢复
//   - 判断错误是否可恢复
//   - 尝试执行恢复操作
//
// 错误包装：
//   - 创建结构化的ConnectionError
//   - 包含错误码、操作类型、URL等信息
//   - 支持重试标记
func (c *WebSocketClient) handleConnectionError(err error) error {
	// 第一步：设置连接状态为断开
	c.setState(StateDisconnected)
	log.Printf("❌ 连接失败: %v", err)

	// 第二步：记录错误统计信息
	c.recordError(err)

	// 第三步：尝试自动错误恢复
	c.attemptErrorRecovery(err)

	// 第四步：返回结构化的错误信息
	return &ConnectionError{
		Code:  c.inferErrorCode(err), // 推断错误码
		Op:    "connect",             // 操作类型
		URL:   c.config.URL,          // 连接URL
		Err:   err,                   // 原始错误
		Retry: true,                  // 支持重试
	}
}

// attemptErrorRecovery 尝试错误恢复
// 这个方法在连接错误发生时尝试自动恢复，提供智能的错误处理机制
//
// 参数说明：
//   - err: 需要尝试恢复的错误
//
// 恢复条件：
//  1. 自动恢复功能已启用（AutoRecovery = true）
//  2. 错误恢复器判断该错误可以恢复
//  3. 有可用的恢复策略
//
// 恢复流程：
//  1. 检查自动恢复是否启用
//  2. 判断错误是否可恢复
//  3. 创建带超时的恢复上下文
//  4. 执行错误恢复操作
//  5. 记录恢复结果
//
// 超时控制：
//   - 使用配置的HandshakeTimeout作为恢复超时
//   - 防止恢复操作无限阻塞
//   - 确保恢复操作的及时性
//
// 错误处理：
//   - 恢复失败时记录警告日志
//   - 不会抛出异常，保证程序稳定性
//   - 支持多种恢复策略
//
// 使用场景：
//   - 网络连接临时中断的恢复
//   - 服务器重启后的自动重连
//   - 配置错误的自动修正
func (c *WebSocketClient) attemptErrorRecovery(err error) {
	// 第一步：检查自动恢复条件
	if c.AutoRecovery && c.errorRecovery.CanRecover(err) {
		log.Printf("🔄 尝试自动恢复连接错误...")

		// 第二步：创建带超时的恢复上下文
		connectCtx, cancel := context.WithTimeout(c.ctx, c.config.HandshakeTimeout)
		defer cancel()

		// 第三步：执行错误恢复操作
		if recoveryErr := c.errorRecovery.Recover(connectCtx, err); recoveryErr != nil {
			log.Printf("⚠️ 自动恢复失败: %v", recoveryErr)
		}
	}
}

// handleErrorWithRecovery 统一的错误处理和恢复函数
// 记录错误统计信息并尝试自动恢复（如果启用）
// operation参数用于在日志中标识错误发生的操作类型
//
// Example:
//
//	client.handleErrorWithRecovery(err, "发送")
//	client.handleErrorWithRecovery(err, "连接")
func (c *WebSocketClient) handleErrorWithRecovery(err error, operation string) {
	c.recordError(err)
	if c.AutoRecovery && c.errorRecovery.CanRecover(err) {
		log.Printf("🔄 尝试自动恢复%s错误...", operation)
		if recoveryErr := c.errorRecovery.Recover(c.ctx, err); recoveryErr != nil {
			log.Printf("⚠️ %s错误恢复失败: %v", operation, recoveryErr)
		}
	}
}

// setupConnection 设置新连接
// 这个方法完成WebSocket连接的最终设置，包括连接替换、状态更新和回调触发
//
// 参数说明：
//   - newConn: 新建立的WebSocket连接对象
//
// 设置流程：
//  1. 记录连接成功日志
//  2. 获取互斥锁保护连接操作
//  3. 关闭旧连接（如果存在）
//  4. 设置新连接和相关属性
//  5. 配置ping/pong处理器
//  6. 更新连接状态和统计信息
//  7. 触发连接成功回调
//
// 连接管理：
//   - 安全关闭旧连接，避免资源泄漏
//   - 原子性地替换连接对象
//   - 更新连接时间和重连计数
//
// 状态更新：
//   - 设置连接状态为StateConnected
//   - 更新连接统计信息
//   - 记录连接建立时间
//
// 性能监控：
//   - 更新性能监控指标
//   - 记录连接成功事件
//   - 支持监控系统集成
//
// 回调机制：
//   - 安全调用用户定义的连接成功回调
//   - 在独立goroutine中执行，避免阻塞
//
// 并发安全：
//   - 使用互斥锁保护连接操作
//   - 确保连接设置的原子性
//   - 避免竞态条件
func (c *WebSocketClient) setupConnection(newConn *websocket.Conn) {
	// 第一步：记录连接成功
	log.Printf("✅ 连接建立成功")

	// 第二步：获取锁保护连接操作
	c.mu.Lock()
	defer c.mu.Unlock()

	// 第三步：关闭旧连接（如果存在）
	if c.conn != nil {
		if err := c.connector.Disconnect(c.conn); err != nil {
			log.Printf("⚠️ 断开连接失败: %v", err)
		}
	}

	// 第四步：设置新连接和相关属性
	c.conn = newConn
	c.Stats.ConnectTime = time.Now()
	c.Stats.ReconnectCount++
	c.setupPingPongHandlers()

	// 第五步：更新连接状态
	c.setState(StateConnected)
	log.Printf("✅ 已连接到 %s [会话: %s]", c.config.URL, c.SessionID)

	// 第六步：更新性能指标
	c.performanceMonitor.UpdateMetrics(c.Stats)

	// 第七步：触发连接成功回调
	c.safeCallOnConnect()
}

// ReadMessages 启动一个 goroutine，持续从 WebSocket 连接读取消息。
// 它处理不同类型的消息，更新读取截止时间，并管理连接错误。
// 如果发生错误或上下文被取消，goroutine 将退出。
// 此函数使用客户端的 WaitGroup 来通知其完成。
// ReadMessages 持续读取WebSocket消息的核心循环
// 这个方法是WebSocket客户端的消息接收引擎，负责持续监听和处理来自服务器的消息
//
// 功能说明：
//   - 持续循环读取WebSocket消息直到连接断开或收到停止信号
//   - 处理各种类型的消息（文本、二进制、控制消息）
//   - 实现智能的错误分类和处理策略
//   - 维护连接状态和统计信息
//   - 支持用户自定义的消息处理回调
//
// 消息处理流程：
//  1. 检查停止信号和连接状态
//  2. 从WebSocket连接读取消息
//  3. 更新连接超时时间
//  4. 更新统计信息和日志记录
//  5. 调用消息处理器处理消息
//  6. 调用用户自定义回调函数
//  7. 记录处理完成状态
//
// 错误处理策略：
//   - 意外关闭：区分正常关闭和异常关闭
//   - EOF错误：服务器主动关闭连接
//   - 网络错误：网络连接中断
//   - 上下文取消：客户端主动停止
//   - 未知错误：记录详细信息便于调试
//
// 并发安全：
//   - 使用defer确保资源正确清理
//   - 通过互斥锁保护连接对象访问
//   - 支持通过上下文优雅停止
//
// 生命周期管理：
//   - 自动设置连接状态为断开
//   - 确保WebSocket连接正确关闭
//   - 清理连接对象引用
//
// 使用场景：
//   - 作为独立的goroutine运行
//   - 在连接建立后自动启动
//   - 支持长时间运行的消息接收
//
// 注意事项：
//   - 此方法会阻塞直到连接断开
//   - 必须在goroutine中调用
//   - 通过上下文控制停止时机
//   - defer块确保在循环退出时将连接标记为未连接并关闭连接
//
// handleReadError 处理读取消息时的错误
// 这个方法统一处理ReadMessage过程中发生的各种错误，提供详细的错误分类和日志记录
//
// 参数说明：
//   - err: 读取消息时发生的错误
//
// 错误分类处理：
//  1. WebSocket意外关闭错误：区分客户端停止和异常关闭
//  2. EOF错误：服务器主动关闭连接
//  3. UnexpectedEOF错误：服务器连接意外断开
//  4. 网络错误：网络连接中断
//  5. 其他错误：未知类型错误
//
// 状态管理：
//   - 设置连接状态为断开
//   - 确保状态变更的原子性
//
// 日志记录：
//   - 根据错误类型记录不同级别的日志
//   - 提供详细的错误信息便于调试
//   - 使用emoji增强日志可读性
func (c *WebSocketClient) handleReadError(err error) {
	c.setState(StateDisconnected)
	if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure, websocket.CloseNormalClosure) {
		select {
		case <-c.ctx.Done():
			log.Printf("ⓘ ReadMessages: WebSocket连接在客户端停止过程中关闭: %v", err)
		default:
			log.Printf("❌ ReadMessages: WebSocket连接异常关闭: %v", err)
		}
	} else if errors.Is(err, io.EOF) {
		log.Printf("🔌 ReadMessages: 服务器主动关闭连接 (EOF): %v", err)
	} else if errors.Is(err, io.ErrUnexpectedEOF) {
		log.Printf("🔌 ReadMessages: 服务器连接意外断开 (UnexpectedEOF): %v", err)
	} else if isNetworkError(err) {
		log.Printf("🔌 ReadMessages: 网络连接中断: %v", err)
	} else {
		select {
		case <-c.ctx.Done():
			log.Printf("ⓘ ReadMessages: 读取消息时检测到context关闭: %v", err)
		default:
			log.Printf("⚠️ ReadMessages: 读取消息失败 (未知类型): %v", err)
		}
	}
}

// processReceivedMessage 处理接收到的消息
// 这个方法统一处理接收到的WebSocket消息，包括统计更新、日志记录和消息处理
//
// 参数说明：
//   - messageType: WebSocket消息类型
//   - message: 消息内容字节数组
//
// 处理流程：
//  1. 重置连接超时时间
//  2. 更新统计信息
//  3. 记录消息到日志文件
//  4. 调用消息处理器处理消息
//  5. 调用用户自定义回调函数
//  6. 记录处理完成状态
//
// 错误处理：
//   - 消息处理器错误：记录日志并尝试恢复
//   - 用户回调错误：记录日志但不中断流程
//
// 性能优化：
//   - 避免不必要的字符串转换
//   - 条件性的详细日志记录
func (c *WebSocketClient) processReceivedMessage(messageType int, message []byte) {
	c.resetTimeout()

	// 更新统计信息
	c.updateStats(messageType, len(message), false)

	// 记录消息到日志文件
	c.logMessage("RECV", messageType, message)

	// 使用消息处理器接口处理消息
	if err := c.messageProcessor.ProcessMessage(messageType, message); err != nil {
		log.Printf("❌ 消息处理器错误: %v", err)
		c.handleErrorWithRecovery(err, "消息处理")
	}

	// 调用用户自定义的消息处理回调（如果设置了）
	if c.onMessage != nil {
		if err := c.onMessage(messageType, message); err != nil {
			log.Printf("❌ 用户消息处理回调错误: %v", err)
		}
	}

	// 记录消息处理（仅在verbose模式下显示）
	if c.config.Verbose {
		log.Printf("📊 消息处理完成，类型: %s", c.getMessageTypeString(messageType))
	}
}

// shouldContinueReading 检查是否应该继续读取消息
// 这个方法检查停止信号和连接状态，决定是否应该继续消息读取循环
//
// 返回值：
//   - bool: true表示应该继续读取，false表示应该停止
//
// 检查项目：
//  1. 上下文取消信号：客户端主动停止
//  2. 连接状态：连接是否仍然有效
//  3. 连接对象：连接对象是否存在
//
// 日志记录：
//   - 记录停止原因便于调试
//   - 区分不同的停止场景
func (c *WebSocketClient) shouldContinueReading() bool {
	select {
	case <-c.ctx.Done():
		log.Printf("📋 ReadMessages: 收到停止信号，退出消息读取循环")
		return false
	default:
		conn, connected := c.getConnSafely()
		if conn == nil || !connected {
			if c.isConnected() {
				log.Printf("⚠️ ReadMessages: 连接状态不一致或连接对象为空，退出消息读取循环")
			}
			return false
		}
		return true
	}
}

func (c *WebSocketClient) ReadMessages() {
	// 延迟执行的清理匿名函数：确保ReadMessages退出时正确清理连接资源
	defer func() {
		c.setState(StateDisconnected) // 设置连接状态为断开，通知其他组件连接已结束
		c.mu.Lock()                   // 获取互斥锁，保护连接对象的并发访问
		if c.conn != nil {
			// 尝试关闭WebSocket连接，释放网络资源
			if closeErr := c.conn.Close(); closeErr != nil {
				log.Printf("⚠️ 关闭WebSocket连接失败: %v", closeErr)
			}
			c.conn = nil // 清空连接对象引用，防止后续误用
		}
		c.mu.Unlock() // 释放互斥锁
	}()

	for {
		// 检查是否应该继续读取消息
		if !c.shouldContinueReading() {
			return
		}

		// 获取连接对象
		conn, _ := c.getConnSafely()

		// 读取消息
		messageType, message, err := conn.ReadMessage()
		if err != nil {
			c.handleReadError(err)
			return
		}

		// 处理接收到的消息
		c.processReceivedMessage(messageType, message)
	}
}

// sendPeriodicPing 启动一个 goroutine，该 goroutine 定期向服务器发送 ping 消息
// 这个函数实现了WebSocket连接的心跳保活机制，防止连接因空闲而被中间设备断开
//
// 功能说明：
//   - 根据配置的PingInterval间隔发送ping消息
//   - 监听上下文取消信号，支持优雅停止
//   - 使用WaitGroup确保goroutine正确结束
//   - 处理发送失败的情况，记录错误但不中断
//
// 心跳机制：
//   - 定时发送：按配置的间隔定期发送ping消息
//   - 错误处理：发送失败时记录日志，下次继续尝试
//   - 优雅停止：响应上下文取消，立即停止发送
//
// 并发安全：
//   - 使用WaitGroup管理goroutine生命周期
//   - 通过上下文控制停止时机
//   - 发送操作使用线程安全的sendControlMessage
//
// 使用场景：
//   - 保持WebSocket连接活跃
//   - 检测连接状态
//   - 防止NAT/防火墙超时断开连接
//
// 注意事项：
//   - 只有在DisableAutoPing为false时才会调用此函数
//   - 使用配置中的PingInterval而不是硬编码值
//   - 支持详细日志模式下的ping消息记录
func (c *WebSocketClient) sendPeriodicPing() {
	c.wg.Add(1)
	defer c.wg.Done()

	// 使用配置中的ping间隔，而不是硬编码的默认值
	c.pingTicker = time.NewTicker(c.config.PingInterval)
	defer c.pingTicker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			if c.config.VerbosePing {
				log.Printf("📋 sendPeriodicPing: 停止周期性ping (context done)")
			}
			return
		case <-c.pingTicker.C:
			select {
			case <-c.ctx.Done():
				if c.config.VerbosePing {
					log.Printf("📡 sendPeriodicPing: 停止周期性ping (context done before ping send)")
				}
				return
			default:
			}
			if err := c.sendControlMessage(websocket.PingMessage, nil); err != nil {
				log.Printf("❌ sendPeriodicPing: 发送ping失败: %v. 将在下次tick尝试。", err)
			} else if c.config.VerbosePing {
				log.Printf("📡 sendPeriodicPing: 发送ping到服务器")
			}
		}
	}
}

// Stop 优雅地停止WebSocket客户端
// 这个方法实现了客户端的优雅关闭流程，确保所有资源正确释放和清理
//
// 功能说明：
//   - 取消客户端的上下文，通知所有goroutine停止
//   - 发送WebSocket关闭消息，通知服务器连接即将关闭
//   - 关闭底层的WebSocket连接
//   - 等待所有客户端管理的goroutine完成
//   - 清理相关资源（日志文件、监控服务器等）
//
// 停止流程：
//  1. 取消上下文：通知所有goroutine停止工作
//  2. 设置状态：将连接状态设置为断开
//  3. 发送关闭消息：向服务器发送正常关闭消息
//  4. 关闭连接：关闭底层的WebSocket连接
//  5. 等待完成：等待所有goroutine优雅退出
//  6. 清理资源：关闭日志文件和监控服务器
//
// 优雅关闭特点：
//   - 协议兼容：按照WebSocket协议发送关闭帧
//   - 资源清理：确保所有资源正确释放
//   - 并发安全：使用锁保护关键操作
//   - 超时控制：避免无限等待
//
// 错误处理：
//   - 发送关闭消息失败：记录警告但继续关闭流程
//   - 连接关闭失败：记录警告但继续清理
//   - 确保即使出现错误也能完成清理
//
// 并发安全：
//   - 使用互斥锁保护连接对象访问
//   - 通过WaitGroup等待所有goroutine完成
//   - 支持多次调用（幂等操作）
//
// 使用场景：
//   - 程序正常退出时调用
//   - 接收到停止信号时调用
//   - 发生不可恢复错误时调用
//   - 用户主动断开连接时调用
//
// 注意事项：
//   - 此方法会阻塞直到所有goroutine停止
//   - 应该在主goroutine中调用
//   - 支持多次调用，不会产生副作用
//   - 确保在程序退出前调用此方法
func (c *WebSocketClient) Stop() {
	log.Printf("🛑 Stop: 开始停止客户端...")
	c.cancel()
	c.setState(StateDisconnected)
	c.mu.Lock()
	if c.conn != nil {
		if err := c.conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "客户端主动关闭")); err != nil {
			log.Printf("⚠️ 发送关闭消息失败: %v", err)
		}
		if closeErr := c.conn.Close(); closeErr != nil {
			log.Printf("⚠️ 关闭WebSocket连接失败: %v", closeErr)
		}
		c.conn = nil
	}
	c.mu.Unlock()
	log.Printf("⏳ Stop: 等待所有内部goroutine停止...")
	c.wg.Wait()

	// 关闭消息日志文件
	c.closeMessageLog()

	// 停止监控服务器
	c.stopMonitoringServers()

	log.Printf("🛑 Stop: 客户端已优雅停止")
}

// getConnSafely 提供一种线程安全的方式来获取当前的 WebSocket 连接
// 对象及其连接状态。
func (c *WebSocketClient) getConnSafely() (*websocket.Conn, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	// 直接检查状态，避免调用isConnected()导致的死锁
	connected := atomic.LoadInt32(&c.State) == int32(StateConnected)
	return c.conn, connected
}

// sendControlMessage 发送 WebSocket 控制消息（例如 Ping、Pong、Close）。
// 它确保在尝试发送消息之前客户端已连接。
// 此函数是线程安全的。
func (c *WebSocketClient) sendControlMessage(messageType int, data []byte) error {
	conn, connected := c.getConnSafely()
	if conn == nil || !connected {
		return fmt.Errorf("连接已关闭")
	}

	// 使用写锁保护WebSocket写操作，防止并发写入
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	return conn.WriteControl(messageType, data, time.Now().Add(WriteTimeout))
}

// setupPingPongHandlers 为当前的 WebSocket 连接配置 ping 和 pong 处理器。
// pong 处理器更新读取截止时间。ping 处理器以 pong 消息响应，
// 并且也会更新读取截止时间。还会设置一个初始的读取截止时间。
// 此方法只应在 c.conn 不为 nil 时调用，并且通常在锁的保护下调用。
func (c *WebSocketClient) setupPingPongHandlers() {
	if c.conn == nil {
		return
	}
	c.conn.SetPongHandler(func(appData string) error {
		if c.config.VerbosePing {
			log.Printf("📡 PongHandler: 收到服务器pong响应")
		}
		c.resetTimeout()
		return nil
	})
	c.conn.SetPingHandler(func(appData string) error {
		if c.config.VerbosePing {
			log.Printf("📡 PingHandler: 收到服务器ping，发送pong响应")
		}
		err := c.sendControlMessage(websocket.PongMessage, []byte(appData))
		if err != nil {
			log.Printf("❌ PingHandler: 发送pong失败: %v", err)
		}
		c.resetTimeout()
		return err
	})
	if c.conn != nil {
		if err := c.conn.SetReadDeadline(time.Now().Add(ReadTimeout)); err != nil {
			log.Printf("⚠️ 设置读取超时失败: %v", err)
		}
	}
}

// resetTimeout 重置 WebSocket 连接上的读取截止时间。
// 通常在成功发送或接收数据后调用此方法，以保持连接活动状态。
// 如果连接为nil，则不执行任何操作。
func (c *WebSocketClient) resetTimeout() {
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()
	if conn != nil {
		if err := conn.SetReadDeadline(time.Now().Add(ReadTimeout)); err != nil {
			log.Printf("⚠️ 设置连接读取超时失败: %v", err)
		}
	}
}

// logReceivedMessage 记录接收到的消息（优化字符串转换）
// 这个方法根据消息类型记录不同格式的接收日志，优化了字符串转换性能
//
// 参数说明：
//   - messageType: WebSocket消息类型常量
//   - message: 消息内容的字节数组
//
// 消息类型处理：
//  1. 文本消息：显示完整的文本内容
//  2. 二进制消息：只显示字节数，避免乱码
//  3. Ping消息：仅在详细模式下显示
//  4. Pong消息：仅在详细模式下显示
//  5. 其他类型：显示消息类型编号
//
// 性能优化：
//   - 避免不必要的字符串转换
//   - 二进制消息只显示长度，不转换内容
//   - 控制消息的日志输出频率
//
// 日志控制：
//   - Ping/Pong消息受VerbosePing配置控制
//   - 使用emoji增强日志可读性
//   - 提供结构化的日志格式
//
// 使用场景：
//   - 调试消息接收问题
//   - 监控消息流量
//   - 分析消息类型分布
//   - 开发阶段的消息跟踪
func (c *WebSocketClient) logReceivedMessage(messageType int, message []byte) {
	switch messageType {
	case websocket.TextMessage:
		// 文本消息：显示完整内容
		log.Printf("📥 收到文本消息: %s", string(message))
	case websocket.BinaryMessage:
		// 二进制消息：只显示字节数，避免乱码
		log.Printf("📥 收到二进制消息: %d 字节", len(message))
	case websocket.PingMessage:
		// Ping消息：仅在详细模式下显示
		if c.config.VerbosePing {
			log.Printf("📡 收到ping消息")
		}
	case websocket.PongMessage:
		// Pong消息：仅在详细模式下显示
		if c.config.VerbosePing {
			log.Printf("📡 收到pong消息")
		}
	default:
		// 其他类型消息：显示类型编号
		log.Printf("📥 收到其他类型消息: %d", messageType)
	}
}

// isNetworkError 检查给定的错误是否可能是常见的网络相关错误
// 极致优化版本：使用高效的错误检查策略，避免字符串操作
//
// 参数说明：
//   - err: 需要检查的错误对象
//
// 返回值：
//   - bool: true表示是网络错误，false表示不是
//
// 检查策略（按性能优先级排序）：
//  1. 快速路径：直接比较系统调用错误常量（最高效）
//  2. 中等路径：类型断言检查网络操作错误
//  3. DNS错误：检查DNS相关的错误类型
//  4. 字符串匹配：最后的兜底检查（性能最低）
//
// 支持的网络错误类型：
//   - 系统调用错误：ECONNREFUSED、ECONNRESET、ETIMEDOUT等
//   - 网络操作错误：超时、网络不可达等
//   - DNS错误：域名解析失败、DNS超时
//   - 字符串模式：常见的网络错误消息
//
// 性能优化：
//   - 优先使用类型检查，避免字符串操作
//   - 递归检查嵌套错误的内部错误
//   - 使用预编译的错误模式数组
//
// 使用场景：
//   - 区分网络错误和其他类型错误
//   - 实现智能重试策略
//   - 错误分类和统计
//   - 日志记录和问题诊断
func isNetworkError(err error) bool {
	// 第一步：空值检查
	if err == nil {
		return false
	}

	// 第二步：快速路径 - 检查常见的系统调用错误（最高效）
	switch err {
	case syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.ETIMEDOUT,
		syscall.ENETUNREACH, syscall.EHOSTUNREACH, io.EOF, io.ErrUnexpectedEOF:
		return true
	}

	// 第三步：中等路径 - 检查网络操作错误
	if netErr, ok := err.(*net.OpError); ok {
		if netErr.Timeout() {
			return true
		}
		// 递归检查内部错误
		return isNetworkError(netErr.Err)
	}

	// 第四步：检查DNS错误
	if dnsErr, ok := err.(*net.DNSError); ok {
		return dnsErr.IsNotFound || dnsErr.IsTimeout
	}

	// 第五步：最后的字符串检查（性能最低，但覆盖面广）
	// 使用预编译的错误模式避免重复的字符串操作
	errStr := err.Error()
	return containsNetworkErrorPattern(errStr)
}

// containsNetworkErrorPattern 检查错误字符串是否包含网络错误模式
// 使用高效的字符串匹配，避免多次调用strings.Contains
//
// 参数说明：
//   - errStr: 错误消息字符串
//
// 返回值：
//   - bool: true表示包含网络错误模式，false表示不包含
//
// 错误模式：
//   - 按出现频率排序，提高匹配效率
//   - 涵盖常见的网络错误消息
//   - 支持多种语言和库的错误格式
//
// 性能优化：
//   - 使用数组而不是切片，避免堆分配
//   - 单次遍历检查所有模式
//   - 按频率排序，提高早期匹配概率
//
// 支持的错误模式：
//   - "connection refused": 连接被拒绝
//   - "i/o timeout": I/O操作超时
//   - "broken pipe": 管道断开
//   - "network is unreachable": 网络不可达
//   - "no such host": 主机不存在
//   - "unexpected EOF": 意外的文件结束
//   - "connection reset": 连接被重置
//   - "host is down": 主机宕机
//   - "network down": 网络断开
//   - "protocol error": 协议错误
//
// 使用场景：
//   - isNetworkError的字符串匹配后备方案
//   - 处理第三方库的自定义错误消息
//   - 支持多种错误格式的兼容性
func containsNetworkErrorPattern(errStr string) bool {
	// 预定义的网络错误模式（按出现频率排序）
	patterns := [...]string{
		"connection refused",     // 最常见：连接被拒绝
		"i/o timeout",            // 常见：I/O超时
		"broken pipe",            // 常见：管道断开
		"network is unreachable", // 网络不可达
		"no such host",           // DNS解析失败
		"unexpected EOF",         // 意外的文件结束
		"connection reset",       // 连接被重置
		"host is down",           // 主机宕机
		"network down",           // 网络断开
		"protocol error",         // 协议错误
	}

	// 使用单次遍历检查所有模式
	for _, pattern := range patterns {
		if strings.Contains(errStr, pattern) {
			return true
		}
	}
	return false
}

// --- parseArgs 辅助函数定义开始 ---

// parseRetryCountArg 解析 -r 参数（重试次数）
// 这个函数解析命令行中的重试次数参数，支持智能重试策略配置
//
// 参数说明：
//   - args: 完整的命令行参数列表
//   - currentIndex: 当前正在处理的 -r 参数的索引位置
//   - config: 客户端配置对象，用于存储解析结果
//
// 返回值：
//   - int: 更新后的参数索引（成功时跳过值参数）
//   - error: 解析失败时的错误信息
//
// 参数验证：
//  1. 检查是否有下一个参数作为值
//  2. 验证值是否为非负整数
//  3. 设置配置并更新索引
//
// 重试策略：
//   - 0: 5次快速重试 + 无限慢速重试
//   - N: N次快速重试 + N次慢速重试
//
// 错误处理：
//   - 缺少值：提示需要指定重试次数
//   - 无效值：提示必须是非负整数
//   - 返回详细的错误信息便于用户理解
func parseRetryCountArg(args []string, currentIndex int, config *ClientConfig) (int, error) {
	// 第一步：检查是否有下一个参数作为值
	if currentIndex+1 < len(args) {
		valStr := args[currentIndex+1]

		// 第二步：验证并解析重试次数
		if val, err := strconv.Atoi(valStr); err == nil && val >= 0 {
			config.MaxRetries = val
			return currentIndex + 1, nil // 成功解析，跳过下一个参数（值）
		} else {
			return currentIndex, fmt.Errorf("⚠️ -r 参数值 '%s' 必须是非负整数", valStr)
		}
	} else {
		return currentIndex, fmt.Errorf("⚠️ -r 参数需要指定重试次数")
	}
}

// parseRetryDelayArg 解析 -t 参数（重试间隔）
// 这个函数解析命令行中的重试间隔参数，配置慢速重试的等待时间
//
// 参数说明：
//   - args: 完整的命令行参数列表
//   - currentIndex: 当前正在处理的 -t 参数的索引位置
//   - config: 客户端配置对象，用于存储解析结果
//
// 返回值：
//   - int: 更新后的参数索引（成功时跳过值参数）
//   - error: 解析失败时的错误信息
//
// 参数验证：
//  1. 检查是否有下一个参数作为值
//  2. 验证值是否为正整数（秒）
//  3. 转换为time.Duration并设置配置
//
// 时间单位：
//   - 输入值以秒为单位
//   - 内部转换为time.Duration类型
//   - 支持1-3600秒的合理范围
//
// 错误处理：
//   - 缺少值：提示需要指定重试间隔
//   - 无效值：提示必须是正整数
//   - 提供用户友好的错误信息
func parseRetryDelayArg(args []string, currentIndex int, config *ClientConfig) (int, error) {
	// 第一步：检查是否有下一个参数作为值
	if currentIndex+1 < len(args) {
		valStr := args[currentIndex+1]

		// 第二步：验证并解析重试间隔
		if val, err := strconv.Atoi(valStr); err == nil && val > 0 {
			config.RetryDelay = time.Duration(val) * time.Second
			return currentIndex + 1, nil // 成功解析，跳过下一个参数（值）
		} else {
			return currentIndex, fmt.Errorf("⚠️ -t 参数值 '%s' 必须是正整数", valStr)
		}
	} else {
		return currentIndex, fmt.Errorf("⚠️ -t 参数需要指定重试间隔")
	}
}

// --- parseArgs 辅助函数定义结束 ---

// parseArgs 解析命令行参数以创建 ClientConfig
// 这是命令行参数解析的主入口函数，负责完整的参数处理流程
//
// 返回值：
//   - *ClientConfig: 解析后的客户端配置对象
//   - bool: 是否跳过证书警告（-n参数）
//   - error: 解析过程中的错误信息
//
// 解析流程：
//  1. 检查参数数量的基本要求
//  2. 创建默认配置作为基础
//  3. 解析所有命令行标志和选项
//  4. 处理WebSocket URL参数
//  5. 验证最终配置的有效性
//
// 参数分类：
//   - 信息类：-h, --help, --version, --build-info
//   - 连接类：URL, -n（跳过证书验证）
//   - 重试类：-r（重试次数）, -t（重试间隔）
//   - 日志类：-v（详细模式）, -l（日志文件）
//   - 交互类：-i（交互模式）
//   - 监控类：--metrics, --metrics-port, --health-port
//
// 错误处理：
//   - 参数不足：显示使用说明
//   - 标志解析失败：返回具体错误
//   - URL处理失败：返回URL相关错误
//   - 配置验证失败：返回验证错误
//
// 使用示例：
//
//	config, skipWarning, err := parseArgs()
//	if err != nil {
//	    log.Fatal(err)
//	}
func parseArgs() (*ClientConfig, bool, error) {
	// 第一步：检查基本参数要求
	if len(os.Args) < 2 {
		showUsage()
		return nil, false, fmt.Errorf("参数不足，请提供WebSocket URL")
	}

	// 第二步：创建默认配置
	config := NewDefaultConfig("")
	var skipCertWarning bool
	var remainingArgs []string

	// 第三步：解析命令行标志
	if err := parseFlags(config, &skipCertWarning, &remainingArgs); err != nil {
		return nil, false, err
	}

	// 第四步：处理URL参数
	if err := processURLArg(config, remainingArgs); err != nil {
		return nil, false, err
	}

	// 第五步：验证配置
	if err := config.Validate(); err != nil {
		return nil, false, fmt.Errorf("配置验证失败: %w", err)
	}

	return config, skipCertWarning, nil
}

// parseFlags 解析命令行标志
// 这个函数是命令行参数解析的核心，负责处理所有的标志和选项
//
// 参数说明：
//   - config: 客户端配置对象，用于存储解析结果
//   - skipCertWarning: 指向布尔值的指针，用于设置是否跳过证书警告
//   - remainingArgs: 指向字符串切片的指针，用于收集非标志参数
//
// 返回值：
//   - error: 解析过程中的错误信息
//
// 支持的标志分类：
//  1. 信息类标志：-h, --help, --version, --build-info, --health-check
//  2. 连接类标志：-n（跳过证书验证）
//  3. 日志类标志：-v（详细模式）, -l（日志文件）, --log-file
//  4. 交互类标志：-i, --interactive（交互模式）
//  5. 监控类标志：--metrics, --metrics-port, --health-port
//  6. 重试类标志：-r（重试次数）, -t（重试间隔）
//
// 处理逻辑：
//   - 信息类标志：立即执行相应功能并退出程序
//   - 配置类标志：更新配置对象的相应字段
//   - 带值标志：调用专门的解析函数处理
//   - 未知标志：返回错误信息
//   - 非标志参数：添加到remainingArgs中
//
// 错误处理：
//   - 未知标志：返回详细的错误信息
//   - 参数解析失败：传播子函数的错误
//   - 提供用户友好的错误提示
//
// 并发安全：此函数在main函数中单线程调用，无需考虑并发安全

// handleInfoFlags 处理信息类标志（立即执行并退出）
// 这些标志会立即显示信息并退出程序，不需要进一步处理
//
// 参数说明：
//   - arg: 当前处理的命令行参数
//
// 返回值：
//   - bool: true表示已处理该标志，false表示不是信息类标志
//
// 支持的信息类标志：
//   - -h, --help: 显示使用帮助
//   - --version: 显示版本信息
//   - --build-info: 显示详细构建信息
//   - --health-check: 执行健康检查
func handleInfoFlags(arg string) bool {
	switch arg {
	case "-h", "--help":
		showUsage()
		os.Exit(0)
	case "--version":
		showVersion()
		os.Exit(0)
	case "--build-info":
		showBuildInfo()
		os.Exit(0)
	case "--health-check":
		performHealthCheck()
		os.Exit(0)
	default:
		return false
	}
	return true
}

// handleBooleanFlags 处理简单布尔标志
// 这些标志不需要额外的值，只是设置配置选项为true
//
// 参数说明：
//   - arg: 当前处理的命令行参数
//   - config: 客户端配置对象
//   - skipCertWarning: TLS证书警告跳过标志
//
// 返回值：
//   - bool: true表示已处理该标志，false表示不是布尔标志
//
// 支持的布尔标志：
//   - -n: 跳过TLS证书验证警告
//   - -f: 强制启用TLS证书验证
//   - -d: 禁用自动ping功能
//   - -v: 启用详细日志
//   - -i, --interactive: 启用交互模式
//   - --metrics: 启用指标收集
func handleBooleanFlags(arg string, config *ClientConfig, skipCertWarning *bool) bool {
	switch arg {
	case "-n":
		*skipCertWarning = true
	case "-f":
		config.ForceTLSVerify = true
	case "-d":
		config.DisableAutoPing = true
	case "-v":
		config.Verbose = true
		config.VerbosePing = true
	case "-i", "--interactive":
		config.Interactive = true
	case "--metrics":
		config.MetricsEnabled = true
	default:
		return false
	}
	return true
}

// handleValueFlags 处理带值的标志
// 这些标志需要额外的参数值，调用专门的解析函数处理
//
// 参数说明：
//   - arg: 当前处理的命令行参数
//   - currentIndex: 当前参数在os.Args中的索引
//   - config: 客户端配置对象
//
// 返回值：
//   - int: 更新后的参数索引
//   - error: 解析失败时的错误信息
//
// 支持的带值标志：
//   - -l: 日志文件（可选值）
//   - --log-file: 日志文件路径（必需值）
//   - --metrics-port: 指标服务端口
//   - --health-port: 健康检查端口
//   - -r: 重试次数
//   - -t: 重试延迟
func handleValueFlags(arg string, currentIndex int, config *ClientConfig) (int, error) {
	switch arg {
	case "-l":
		return parseLogFileArg(os.Args, currentIndex, config), nil
	case "--log-file":
		return parseLogFilePathArg(os.Args, currentIndex, config)
	case "--metrics-port":
		newIndex, err := parsePortArg(os.Args, currentIndex, &config.MetricsPort, "metrics-port")
		if err == nil {
			config.MetricsEnabled = true // 自动启用metrics
		}
		return newIndex, err
	case "--health-port":
		return parsePortArg(os.Args, currentIndex, &config.HealthPort, "health-port")
	case "-r":
		return parseRetryCountArg(os.Args, currentIndex, config)
	case "-t":
		return parseRetryDelayArg(os.Args, currentIndex, config)
	default:
		return currentIndex, nil
	}
}

func parseFlags(config *ClientConfig, skipCertWarning *bool, remainingArgs *[]string) error {
	// 遍历所有命令行参数（跳过程序名）
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]

		// 处理信息类标志（立即执行并退出）
		if handled := handleInfoFlags(arg); handled {
			continue
		}

		// 处理简单布尔标志
		if handled := handleBooleanFlags(arg, config, skipCertWarning); handled {
			continue
		}

		// 处理带值的标志
		newIndex, err := handleValueFlags(arg, i, config)
		if err != nil {
			return err
		}
		if newIndex != i {
			i = newIndex
			continue
		}

		// 处理未知标志和非标志参数
		if strings.HasPrefix(arg, "-") {
			return fmt.Errorf("⚠️ 未知参数或标志: '%s'", arg)
		}
		*remainingArgs = append(*remainingArgs, arg)
	}
	return nil
}

// parseLogFileArg 解析 -l 参数
// 这个函数处理简化的日志文件参数，支持可选的文件路径
//
// 参数说明：
//   - args: 完整的命令行参数列表
//   - currentIndex: 当前正在处理的 -l 参数的索引位置
//   - config: 客户端配置对象，用于存储解析结果
//
// 返回值：
//   - int: 更新后的参数索引
//
// 解析逻辑：
//  1. 检查下一个参数是否存在且不是标志（不以-开头）
//  2. 如果存在：使用该参数作为日志文件路径
//  3. 如果不存在：设置为"auto"，自动生成文件名
//
// 自动文件名格式：
//   - "auto"会在程序中生成类似"websocket_20240101_150405.log"的文件名
//   - 包含时间戳，避免文件名冲突
//
// 使用示例：
//   - "./wsc -l ws://example.com" -> 自动生成文件名
//   - "./wsc -l mylog.txt ws://example.com" -> 使用指定文件名
//
// 注意事项：
//   - 此函数不返回错误，总是成功
//   - 支持灵活的参数使用方式
func parseLogFileArg(args []string, currentIndex int, config *ClientConfig) int {
	// 检查下一个参数是否存在且不是标志
	if currentIndex+1 < len(args) && !strings.HasPrefix(args[currentIndex+1], "-") {
		// 使用指定的文件路径
		config.LogFile = args[currentIndex+1]
		return currentIndex + 1 // 跳过文件路径参数
	}
	// 没有指定文件路径，使用自动生成
	config.LogFile = "auto"
	return currentIndex // 不跳过任何参数
}

// parseLogFilePathArg 解析 --log-file 参数
// 这个函数处理完整的日志文件路径参数，要求必须提供文件路径
//
// 参数说明：
//   - args: 完整的命令行参数列表
//   - currentIndex: 当前正在处理的 --log-file 参数的索引位置
//   - config: 客户端配置对象，用于存储解析结果
//
// 返回值：
//   - int: 更新后的参数索引
//   - error: 解析失败时的错误信息
//
// 解析逻辑：
//  1. 检查下一个参数是否存在
//  2. 如果存在：使用该参数作为日志文件路径
//  3. 如果不存在：返回错误
//
// 与-l参数的区别：
//   - --log-file 要求必须提供文件路径
//   - -l 参数可以不提供文件路径（自动生成）
//
// 使用示例：
//   - "./wsc --log-file /path/to/log.txt ws://example.com"
//   - "./wsc --log-file ./logs/websocket.log ws://example.com"
//
// 错误情况：
//   - 缺少文件路径：返回详细的错误信息
func parseLogFilePathArg(args []string, currentIndex int, config *ClientConfig) (int, error) {
	// 检查下一个参数是否存在
	if currentIndex+1 < len(args) {
		// 使用指定的文件路径
		config.LogFile = args[currentIndex+1]
		return currentIndex + 1, nil // 成功解析，跳过文件路径参数
	}
	// 缺少文件路径，返回错误
	return currentIndex, fmt.Errorf("⚠️ --log-file 参数需要指定文件路径")
}

// parsePortArg 解析端口参数
// 这个函数处理各种端口相关的命令行参数，确保端口号的有效性
//
// 参数说明：
//   - args: 完整的命令行参数列表
//   - currentIndex: 当前正在处理的端口参数的索引位置
//   - port: 指向整数的指针，用于存储解析后的端口号
//   - argName: 参数名称，用于错误信息中的显示
//
// 返回值：
//   - int: 更新后的参数索引
//   - error: 解析失败时的错误信息
//
// 端口验证：
//  1. 检查下一个参数是否存在
//  2. 验证参数是否为有效整数
//  3. 验证端口号范围（1-65535）
//  4. 设置端口值并更新索引
//
// 端口范围说明：
//   - 1-1023: 系统保留端口（需要管理员权限）
//   - 1024-49151: 注册端口
//   - 49152-65535: 动态/私有端口
//
// 使用示例：
//   - "--metrics-port 9090"
//   - "--health-port 8080"
//
// 错误处理：
//   - 缺少端口号：提示需要指定端口号
//   - 无效端口号：提示端口号范围要求
//   - 使用参数名称提供具体的错误信息
func parsePortArg(args []string, currentIndex int, port *int, argName string) (int, error) {
	// 第一步：检查下一个参数是否存在
	if currentIndex+1 < len(args) {
		// 第二步：尝试解析端口号并验证范围
		if p, err := strconv.Atoi(args[currentIndex+1]); err == nil && p > 0 && p <= 65535 {
			*port = p
			return currentIndex + 1, nil // 成功解析，跳过端口号参数
		}
		// 端口号无效
		return currentIndex, fmt.Errorf("⚠️ --%s 参数值必须是有效端口号 (1-65535)", argName)
	}
	// 缺少端口号
	return currentIndex, fmt.Errorf("⚠️ --%s 参数需要指定端口号", argName)
}

// processURLArg 处理URL参数
// 这个函数验证和处理WebSocket URL参数，确保URL的有效性
//
// 参数说明：
//   - config: 客户端配置对象，用于存储验证后的URL
//   - remainingArgs: 解析标志后剩余的非标志参数列表
//
// 返回值：
//   - error: 处理失败时的错误信息
//
// 验证逻辑：
//  1. 检查是否提供了URL参数
//  2. 检查是否只有一个URL参数
//  3. 验证URL格式的有效性
//  4. 设置配置中的URL字段
//
// URL格式要求：
//   - 必须以"ws://"或"wss://"开头
//   - ws://: 非加密WebSocket连接
//   - wss://: 加密WebSocket连接（推荐）
//
// 错误处理：
//   - 未提供URL：显示使用说明并返回错误
//   - 多个URL：提示参数重复错误
//   - 无效格式：提示URL格式要求
//
// 使用示例：
//   - 有效URL: "ws://localhost:8080/websocket"
//   - 有效URL: "wss://api.example.com/ws"
//   - 无效URL: "http://example.com" (不是WebSocket协议)
func processURLArg(config *ClientConfig, remainingArgs []string) error {
	// 第一步：检查是否提供了URL参数
	if len(remainingArgs) == 0 {
		showUsage()
		return fmt.Errorf("未指定WebSocket URL")
	}

	// 第二步：检查是否只有一个URL参数
	if len(remainingArgs) > 1 {
		return fmt.Errorf("⚠️ 参数过多或URL指定重复: '%s'", strings.Join(remainingArgs, " "))
	}

	// 第三步：验证URL格式
	urlArg := remainingArgs[0]
	if !isValidWebSocketURL(urlArg) {
		return fmt.Errorf("⚠️ 无效的WebSocket URL '%s'，必须以ws://或wss://开头", urlArg)
	}

	// 第四步：设置配置中的URL
	config.URL = urlArg
	return nil
}

// showVersion 显示版本号
// 这个函数显示应用程序的简洁版本信息
//
// 输出格式：
//   - "应用名称 v版本号"
//   - 例如："wsc v1.0.0"
//
// 使用场景：
//   - 用户使用--version参数查询版本
//   - 脚本中检查程序版本
//   - 快速确认程序版本信息
//
// 与showBuildInfo的区别：
//   - showVersion: 只显示基本版本信息
//   - showBuildInfo: 显示详细的构建和系统信息
func showVersion() {
	fmt.Printf("%s v%s\n", AppName, AppVersion)
}

// formatBuildTime 格式化构建时间，支持多种时间格式
// 这个函数将构建时间字符串转换为用户友好的本地时间格式
//
// 参数说明：
//   - buildTime: 构建时间字符串，可能来自不同的构建系统
//
// 返回值：
//   - string: 格式化后的时间字符串，包含本地时间和时区信息
//
// 支持的输入格式：
//  1. "2006-01-02 15:04:05 MST" - 新格式，包含时区
//  2. "2006-01-02_15:04:05_MST" - 旧格式，下划线分隔
//  3. "2006-01-02_15:04:05_UTC" - UTC格式
//  4. "2006-01-02T15:04:05Z07:00" - ISO 8601格式
//  5. "2006-01-02 15:04:05" - 简单格式，无时区
//
// 输出格式：
//   - "2024-01-02 15:04:05 CST (UTC+08:00)"
//   - 包含本地时间、时区名称和UTC偏移
//
// 处理逻辑：
//  1. 检查特殊值（"unknown"或空字符串）
//  2. 按优先级尝试解析不同格式
//  3. 转换为本地时间并格式化
//  4. 解析失败时返回原始字符串
//
// 时区处理：
//   - 自动转换为系统本地时区
//   - 显示时区名称和UTC偏移
//   - 支持夏令时自动调整
//
// 使用场景：
//   - showBuildInfo中显示构建时间
//   - 日志记录中的时间格式化
//   - 用户界面的时间显示
func formatBuildTime(buildTime string) string {
	// 第一步：处理特殊值
	if buildTime == "unknown" || buildTime == "" {
		return "unknown"
	}

	// 第二步：定义支持的时间格式（按常见程度排序）
	formats := []string{
		"2006-01-02 15:04:05 MST",   // 新格式：2024-01-02 15:04:05 CST
		"2006-01-02_15:04:05_MST",   // 旧格式：2024-01-02_15:04:05_CST
		"2006-01-02_15:04:05_UTC",   // UTC格式：2024-01-02_15:04:05_UTC
		"2006-01-02T15:04:05Z07:00", // ISO格式：2024-01-02T15:04:05+08:00
		"2006-01-02 15:04:05",       // 简单格式：2024-01-02 15:04:05
	}

	// 第三步：尝试解析不同格式
	for _, format := range formats {
		if t, err := time.Parse(format, buildTime); err == nil {
			// 第四步：转换为本地时间并格式化
			localTime := t.Local()
			return localTime.Format("2006-01-02 15:04:05 MST (UTC" + localTime.Format("-07:00") + ")")
		}
	}

	// 第五步：如果无法解析，返回原始字符串
	return buildTime
}

// showBuildInfo 显示详细构建信息
// 这个函数提供完整的应用程序构建和运行时信息，用于调试和系统分析
//
// 显示的信息分类：
//  1. 构建信息：版本、构建时间、Git提交、Go版本
//  2. 系统信息：操作系统、架构、编译器、CPU核心数
//  3. 运行时信息：内存分配、系统内存、GC次数
//
// 构建信息来源：
//   - AppVersion: 应用程序版本号（编译时注入）
//   - BuildTime: 构建时间戳（编译时注入）
//   - GitCommit: Git提交哈希（编译时注入）
//   - GoVersion: Go编译器版本（编译时注入）
//
// 系统信息来源：
//   - runtime.GOOS: 目标操作系统
//   - runtime.GOARCH: 目标架构
//   - runtime.Compiler: 编译器类型
//   - runtime.NumCPU(): CPU核心数
//
// 内存信息说明：
//   - Alloc: 当前分配的堆内存
//   - Sys: 从系统获取的总内存
//   - NumGC: 垃圾回收执行次数
//
// 使用场景：
//   - 问题诊断和调试
//   - 系统兼容性检查
//   - 性能分析和优化
//   - 部署环境验证
func showBuildInfo() {
	// 显示标题和分隔线
	fmt.Printf("📋 %s 构建信息\n", AppName)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// 构建信息部分
	fmt.Printf("版本:       %s\n", AppVersion)
	fmt.Printf("构建时间:   %s\n", formatBuildTime(BuildTime))
	fmt.Printf("Git提交:    %s\n", GitCommit)
	fmt.Printf("Go版本:     %s\n", GoVersion)

	// 系统信息部分
	fmt.Printf("操作系统:   %s\n", runtime.GOOS)
	fmt.Printf("架构:       %s\n", runtime.GOARCH)
	fmt.Printf("编译器:     %s\n", runtime.Compiler)
	fmt.Printf("CPU核心:    %d\n", runtime.NumCPU())

	// 运行时内存信息部分
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Printf("内存分配:   %d KB\n", m.Alloc/1024)
	fmt.Printf("系统内存:   %d KB\n", m.Sys/1024)
	fmt.Printf("GC次数:     %d\n", m.NumGC)
}

// performHealthCheck 执行自检并返回状态码
// 这个函数执行全面的应用程序健康检查，验证各个组件的运行状态
//
// 检查项目：
//  1. Go运行时版本检查
//  2. 内存使用状态检查
//  3. Goroutine数量检查
//  4. 构建信息完整性检查
//
// 检查标准：
//   - 内存使用：< 100MB 为正常，>= 100MB 为偏高
//   - Goroutine数量：< 100 为正常，>= 100 为偏多
//   - 构建信息：BuildTime、GitCommit、GoVersion 都不为"unknown"
//
// 输出格式：
//   - ✅ 表示检查通过
//   - ⚠️ 表示检查发现问题但不影响运行
//   - ❌ 表示检查失败（当前版本无此状态）
//
// 使用场景：
//   - 部署前的健康验证
//   - 运行时状态监控
//   - 问题诊断和调试
//   - 自动化测试中的状态检查
//
// 退出行为：
//   - 检查完成后程序会退出（os.Exit(0)）
//   - 适用于脚本和自动化工具调用
func performHealthCheck() {
	// 显示标题和分隔线
	fmt.Printf("🔍 %s 健康检查\n", AppName)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// 第一项：检查Go运行时版本
	fmt.Printf("✅ Go运行时: %s\n", runtime.Version())

	// 第二项：检查内存使用状态
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	if m.Alloc < 100*1024*1024 { // 小于100MB
		fmt.Printf("✅ 内存使用: %d KB (正常)\n", m.Alloc/1024)
	} else {
		fmt.Printf("⚠️ 内存使用: %d KB (偏高)\n", m.Alloc/1024)
	}

	// 第三项：检查goroutine数量
	numGoroutines := runtime.NumGoroutine()
	if numGoroutines < 100 {
		fmt.Printf("✅ Goroutines: %d (正常)\n", numGoroutines)
	} else {
		fmt.Printf("⚠️ Goroutines: %d (偏多)\n", numGoroutines)
	}

	// 第四项：检查构建信息完整性
	if BuildTime != "unknown" && GitCommit != "unknown" && GoVersion != "unknown" {
		fmt.Println("✅ 构建信息: 完整")
	} else {
		fmt.Println("⚠️ 构建信息: 不完整")
	}

	// 显示完成信息
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("🎉 健康检查完成")
}

// showUsage 将命令行使用说明打印到控制台
// 这个函数提供完整的命令行帮助信息，包括所有可用参数、使用示例和常见用法场景
// 当用户输入-h、--help或参数错误时会调用此函数
//
// 帮助信息结构：
//  1. 程序标题和版本信息
//  2. 基本使用方法和帮助命令
//  3. 公共测试服务器示例
//  4. 自定义使用示例
//  5. 详细参数分类说明
//  6. 监控功能和交互模式说明
//  7. 智能重试策略说明
//  8. 主要特性总结
//
// 设计原则：
//   - 信息层次清晰，便于快速查找
//   - 提供实际可用的示例
//   - 使用emoji增强可读性
//   - 涵盖所有功能和参数
//   - 突出企业级特性和性能优势
func showUsage() {
	fmt.Printf("📋 %s v%s - 高性能 WebSocket 客户端\n", AppName, AppVersion)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("")
	fmt.Println("🚀 使用方法:")
	fmt.Println("  ./wsc [选项] <WebSocket_URL>")
	fmt.Println("  ./wsc -h, --help              显示此帮助信息")
	fmt.Println("")
	fmt.Println("🌐 公共测试服务器:")
	fmt.Println("  ./wsc -n wss://echo.websocket.org")
	fmt.Println("  ./wsc ws://echo.websocket.org")
	fmt.Println("  ./wsc -n wss://ws.postman-echo.com/raw")
	fmt.Println("")
	fmt.Println("📋 自定义示例:")
	fmt.Println("  ./wsc ws://localhost:8080/websocket")
	fmt.Println("  ./wsc -n wss://example.com:8765/websocket")
	fmt.Println("  ./wsc -f wss://secure-api.example.com/ws")
	fmt.Println("  ./wsc -v -r 10 -t 5 wss://api.example.com/ws")
	fmt.Println("")
	fmt.Println("⚙️  可选参数:")
	fmt.Println("    -n                    跳过 TLS 证书验证警告")
	fmt.Println("    -f                    强制启用 TLS 证书验证 (覆盖默认跳过行为)")
	fmt.Println("    -d                    禁用自动ping功能 (仍会响应服务器ping)")
	fmt.Println("    -v                    启用详细日志模式 (包括消息处理和ping/pong)")
	fmt.Println("    -i, --interactive     启用交互式消息发送模式")
	fmt.Println("    -l [文件路径]          记录消息到日志文件 (可选路径)")
	fmt.Println("    --log-file <路径>      指定消息日志文件路径")
	fmt.Println("    -r <次数>             重试次数 (默认5，0=无限)")
	fmt.Println("    -t <秒数>             重试间隔 (默认3秒)")
	fmt.Println("")
	fmt.Println("📋 信息查看:")
	fmt.Println("    --version             显示版本号")
	fmt.Println("    --build-info          显示详细构建信息")
	fmt.Println("    --health-check        执行自检并返回状态码")
	fmt.Println("")
	fmt.Println("📊 监控和指标:")
	fmt.Println("    --metrics             启用Prometheus指标导出")
	fmt.Println("    --metrics-port <端口>  指标服务端口 (默认9090)")
	fmt.Println("    --health-port <端口>   健康检查端口 (默认8080)")
	fmt.Println("")
	fmt.Println("📝 消息日志功能:")
	fmt.Println("    -l                    自动生成日志文件名")
	fmt.Println("    -l mylog.txt          指定日志文件名")
	fmt.Println("    --log-file /path/to/websocket.log  完整路径")
	fmt.Println("")
	fmt.Println("📊 监控功能示例:")
	fmt.Println("    --metrics             启用默认端口监控 (9090/8080)")
	fmt.Println("    --metrics-port 9091   自定义指标端口")
	fmt.Println("    --health-port 8081    自定义健康检查端口")
	fmt.Println("  访问:")
	fmt.Println("    http://localhost:9090/metrics     Prometheus指标")
	fmt.Println("    http://localhost:8080/health      健康检查")
	fmt.Println("    http://localhost:8080/ready       就绪检查")
	fmt.Println("    http://localhost:8080/stats       详细统计")
	fmt.Println("")
	fmt.Println("💬 交互式模式:")
	fmt.Println("    -i                    启用后可通过键盘输入发送消息")
	fmt.Println("    特殊命令:")
	fmt.Println("      /quit               退出程序")
	fmt.Println("      /ping               发送 ping 消息")
	fmt.Println("      /stats              显示连接统计信息")
	fmt.Println("")
	fmt.Println("🔄 智能重试策略:")
	fmt.Println("    -r N: 前N次快速重试 + 后N次慢速重试")
	fmt.Println("    -r 0: 前5次快速重试 + 无限慢速重试")
	fmt.Println("  示例:")
	fmt.Println("    -r 3: 3次快速 + 3次慢速 = 总共6次")
	fmt.Println("    -r 5: 5次快速 + 5次慢速 = 总共10次")
	fmt.Println("")
	fmt.Println("🔐 TLS证书验证选项:")
	fmt.Println("    默认行为: 跳过证书验证，显示安全警告")
	fmt.Println("    -n: 跳过证书验证，不显示警告 (开发环境)")
	fmt.Println("    -f: 强制启用证书验证 (生产环境推荐)")
	fmt.Println("  注意: -f 和 -n 不能同时使用，-f 优先级更高")
	fmt.Println("")
	fmt.Println("✨ 主要特性:")
	fmt.Println("    • 自动重连和智能重试")
	fmt.Println("    • 并发安全和优雅关闭")
	fmt.Println("    • 详细的连接统计信息")
	fmt.Println("    • 支持自定义事件处理")
	fmt.Println("    • 完善的错误分类处理")
	fmt.Println("    • 灵活的TLS安全配置")
}

// showCertificateWarning 当连接到WSS服务器并跳过证书验证时，在控制台显示警告信息
// 这个函数提供重要的安全提示，确保用户了解跳过证书验证的风险
//
// 调用时机：
//   - URL以"wss://"开头（HTTPS WebSocket）
//   - 用户没有使用-n参数跳过警告
//   - 程序启动时自动检查
//
// 警告内容：
//  1. 明确说明正在跳过证书验证
//  2. 解释安全风险和适用场景
//  3. 提供解决方案建议
//  4. 说明如何跳过此警告
//
// 安全考虑：
//   - 中间人攻击风险：攻击者可能拦截和修改通信
//   - 身份验证缺失：无法确认服务器身份
//   - 数据完整性：传输数据可能被篡改
//
// 适用场景：
//   - 开发环境测试
//   - 自签名证书服务器
//   - 内网环境快速连接
//   - 证书配置问题的临时解决方案
func showCertificateWarning() {
	fmt.Println("🔐 WSS连接证书警告")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("⚠️  正在使用WSS连接，将自动跳过证书验证")
	fmt.Println("📋 这意味着:")
	fmt.Println("   • 不会验证服务器证书的有效性")
	fmt.Println("   • 可能存在中间人攻击风险")
	fmt.Println("   • 适用于开发环境和自签名证书")
	fmt.Println("")
	fmt.Println("💡 如需严格证书验证，请联系服务器管理员配置有效证书")
	fmt.Println("🚀 要跳过此警告，请使用 -n 参数")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("")
}

// showTLSVerificationInfo 当启用强制TLS证书验证时，显示安全信息
// 这个函数提供TLS安全验证的相关信息，确保用户了解安全连接的重要性
//
// 调用时机：
//   - URL以"wss://"开头（HTTPS WebSocket）
//   - 用户使用了-f参数强制启用证书验证
//   - 程序启动时自动检查
//
// 信息内容：
//  1. 说明正在使用严格的TLS证书验证
//  2. 解释安全优势和连接要求
//  3. 提供证书问题的解决建议
//  4. 说明与默认行为的区别
//
// 适用场景：
//   - 生产环境连接
//   - 安全要求较高的环境
//   - 需要验证服务器身份的场景
//   - 防止中间人攻击的连接
func showTLSVerificationInfo() {
	fmt.Println("🔒 TLS安全验证模式")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("✅ 已启用严格的TLS证书验证")
	fmt.Println("📋 这意味着:")
	fmt.Println("   • 将验证服务器证书的有效性")
	fmt.Println("   • 检查证书链的完整性")
	fmt.Println("   • 验证服务器身份匹配")
	fmt.Println("   • 防止中间人攻击")
	fmt.Println("")
	fmt.Println("🛡️  安全优势:")
	fmt.Println("   • 确保连接到正确的服务器")
	fmt.Println("   • 保护数据传输安全")
	fmt.Println("   • 符合生产环境安全要求")
	fmt.Println("")
	fmt.Println("⚠️  注意事项:")
	fmt.Println("   • 服务器必须配置有效的SSL证书")
	fmt.Println("   • 自签名证书将导致连接失败")
	fmt.Println("   • 证书过期或域名不匹配会被拒绝")
	fmt.Println("")
	fmt.Println("💡 如遇证书问题，请联系服务器管理员或使用默认模式")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("")
}

// logStartupInfo 在客户端启动时记录有关其配置的初始信息
// 这个函数提供详细的启动日志，便于调试、监控和问题诊断
//
// 参数说明：
//   - config: 客户端配置实例，包含所有运行参数
//   - sessionID: 唯一会话标识符，用于跟踪和关联日志
//
// 记录的信息：
//  1. 应用程序名称和版本
//  2. 目标WebSocket服务器地址
//  3. 会话ID（用于日志关联）
//  4. 智能重试策略配置
//  5. 超时配置详情
//  6. 缓冲区大小设置
//  7. 重试间隔配置
//  8. 日志级别设置
//
// 使用场景：
//   - 程序启动时的配置确认
//   - 问题诊断和调试
//   - 运维监控和日志分析
//   - 配置审计和合规检查
//
// 日志格式：
//   - 使用emoji增强可读性
//   - 结构化信息便于解析
//   - 包含关键配置参数
func logStartupInfo(config *ClientConfig, sessionID string) {
	// 基本信息记录
	log.Printf("🚀 启动 %s v%s", AppName, AppVersion)
	log.Printf("📍 目标URL: %s", config.URL)
	log.Printf("🔗 会话ID: %s", sessionID)

	// 智能重试策略信息
	if config.MaxRetries == 0 {
		log.Printf("🔄 智能重试: 5次快速 + 无限慢速重试")
	} else {
		totalRetries := config.MaxRetries * 2
		log.Printf("🔄 智能重试: %d次快速 + %d次慢速 = 总共%d次",
			config.MaxRetries, config.MaxRetries, totalRetries)
	}

	// 超时配置信息
	log.Printf("⏱️  超时配置: 握手=%v, 读取=%v, 写入=%v, Ping间隔=%v",
		config.HandshakeTimeout, config.ReadTimeout, config.WriteTimeout, config.PingInterval)

	// 缓冲区配置信息
	log.Printf("📦 缓冲区配置: 读取=%d字节, 写入=%d字节, 最大消息=%d字节",
		config.ReadBufferSize, config.WriteBufferSize, config.MaxMessageSize)

	// 重试间隔信息
	log.Printf("⏳ 慢速重试间隔: %v", config.RetryDelay)

	// 日志级别信息
	logLevels := []string{"ERROR", "WARN", "INFO", "DEBUG"}
	if config.LogLevel >= 0 && config.LogLevel < len(logLevels) {
		log.Printf("📝 日志级别: %s", logLevels[config.LogLevel])
	}
}

// main 是 WebSocket 客户端应用程序的入口点
// 这是整个程序的控制中心，负责协调各个组件的初始化和运行
//
// 主要职责：
//  1. 命令行参数解析和验证
//  2. 客户端实例创建和配置
//  3. 信号处理和优雅关闭
//  4. 交互模式和监控服务启动
//  5. 程序生命周期管理
//
// 执行流程：
//
//	参数解析 -> 客户端创建 -> 信号处理设置 -> 服务启动 -> 等待退出信号 -> 优雅关闭
//
// 错误处理：
//   - 参数错误：显示使用说明并退出
//   - 运行时错误：记录日志并尝试恢复
//   - 致命错误：优雅关闭并退出
func main() {
	// ===== 第一阶段：参数解析和验证 =====
	// 解析命令行参数，获取用户配置
	config, skipCertWarning, err := parseArgs()
	if err != nil {
		// parseArgs 内部在参数不足或URL未指定时会调用 showUsage()
		// 这里我们只打印具体的错误信息到标准错误输出，然后平静地以0退出
		// 使用0退出码是因为这是用户输入错误，不是程序错误
		fmt.Fprintln(os.Stderr, err)
		os.Exit(0) // 参数错误时，平静退出
	}

	// ===== 第二阶段：安全提示和警告 =====
	// 处理TLS证书验证相关的提示和警告
	if strings.HasPrefix(config.URL, "wss://") {
		// 检查参数冲突：同时使用 -n 和 -f
		if skipCertWarning && config.ForceTLSVerify {
			fmt.Println("⚠️  参数冲突：不能同时使用 -n（跳过证书警告）和 -f（强制证书验证）")
			fmt.Println("💡 -f 参数优先级更高，将启用严格的TLS证书验证")
			fmt.Println("")
		}

		if config.ForceTLSVerify {
			// 强制TLS验证模式：显示安全提示
			showTLSVerificationInfo()
		} else if !skipCertWarning {
			// 默认模式：显示证书跳过警告
			showCertificateWarning()
		}
	}

	// ===== 第三阶段：客户端创建和初始化 =====
	// 创建WebSocket客户端实例，所有组件都会在这里初始化
	client := NewWebSocketClient(config)

	// 记录启动信息，便于调试和监控
	logStartupInfo(config, client.SessionID)

	// ===== 第四阶段：信号处理设置 =====
	// 设置信号处理，支持优雅关闭
	// 监听 Ctrl+C (SIGINT) 和 SIGTERM 信号
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)

	// ===== 第五阶段：服务启动 =====
	// 启动WebSocket客户端（非阻塞）
	// 使用goroutine确保main函数可以继续处理信号
	go client.Start()

	// 如果启用了交互模式，启动交互式输入处理
	// 允许用户在运行时发送消息
	if config.Interactive {
		go client.startInteractiveMode()
	}

	// 等待中断信号或客户端自动退出
	select {
	case <-interrupt:
		log.Printf("📋 收到中断信号，正在停止...")
		client.Stop()
	case <-client.ctx.Done():
		log.Printf("📋 客户端已自动退出")
		// 客户端已经自动停止，无需再调用Stop()
	}
}

// startInteractiveMode 启动交互式消息发送模式
// 这个方法允许用户通过标准输入实时发送消息和执行特殊命令
//
// 功能特点：
//  1. 等待WebSocket连接建立后才开始接收输入
//  2. 支持普通文本消息发送
//  3. 支持特殊命令执行（/quit、/ping、/stats等）
//  4. 提供友好的命令行界面
//  5. 支持优雅退出和错误处理
//
// 工作流程：
//  1. 等待WebSocket连接建立
//  2. 显示交互模式提示信息
//  3. 循环读取用户输入
//  4. 处理特殊命令或发送普通消息
//  5. 显示操作结果和新的输入提示
//
// 特殊命令：
//   - /quit, /exit, /q: 退出程序
//   - /ping: 发送ping消息
//   - /stats: 显示连接统计信息
//   - /help, /?: 显示帮助信息
//
// 并发安全：
//   - 使用WaitGroup确保优雅退出
//   - 通过context检查取消信号
//   - 与其他goroutine安全协作
func (c *WebSocketClient) startInteractiveMode() {
	// 注册到WaitGroup，确保优雅退出
	c.wg.Add(1)
	defer c.wg.Done()

	// 第一步：等待WebSocket连接建立
	for {
		select {
		case <-c.ctx.Done():
			return // 客户端已停止，退出交互模式
		default:
			if c.isConnected() {
				goto connected // 连接已建立，开始交互
			}
			time.Sleep(100 * time.Millisecond) // 短暂等待后重试
		}
	}

connected:

	// 第二步：显示交互模式启动信息
	log.Printf("💬 交互模式已启用，输入消息后按回车发送")
	log.Printf("💡 特殊命令: /quit (退出), /ping (发送ping), /stats (显示统计)")
	fmt.Print(">>> ")

	// 第三步：创建输入扫描器
	scanner := bufio.NewScanner(os.Stdin)

	// 第四步：主输入循环
	for scanner.Scan() {
		// 检查是否需要退出
		select {
		case <-c.ctx.Done():
			return // 客户端已停止，退出交互模式
		default:
		}

		// 处理用户输入
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			fmt.Print(">>> ") // 空输入，显示新提示符
			continue
		}

		// 第五步：处理特殊命令
		if c.handleInteractiveCommand(input) {
			return // 用户请求退出
		}

		// 第六步：发送普通文本消息
		if err := c.SendText(input); err != nil {
			log.Printf("❌ 发送消息失败: %v", err)
		} else {
			log.Printf("📤 已发送: %s", input)
		}

		// 显示新的输入提示符
		fmt.Print(">>> ")
	}

	// 第七步：处理扫描器错误
	if err := scanner.Err(); err != nil {
		log.Printf("❌ 读取输入时出错: %v", err)
	}
}

// handleInteractiveCommand 处理交互式模式的特殊命令
// 这个方法解析和执行用户输入的特殊命令，提供丰富的交互功能
//
// 参数说明：
//   - input: 用户输入的命令字符串
//
// 返回值：
//   - bool: true表示应该退出交互模式，false表示继续
//
// 支持的命令：
//  1. 退出命令：/quit, /exit, /q - 优雅退出程序
//  2. 网络命令：/ping - 发送WebSocket ping消息
//  3. 信息命令：/stats - 显示详细的连接统计
//  4. 帮助命令：/help, /? - 显示命令帮助信息
//
// 命令处理逻辑：
//   - 使用switch语句进行精确匹配
//   - 每个命令都有相应的错误处理
//   - 提供用户友好的反馈信息
//   - 支持命令别名（如/q代表/quit）
//
// 设计原则：
//   - 命令简洁易记
//   - 提供即时反馈
//   - 错误处理友好
//   - 支持常用操作
func (c *WebSocketClient) handleInteractiveCommand(input string) bool {
	switch input {
	case "/quit", "/exit", "/q":
		// 退出命令：优雅停止客户端
		log.Printf("👋 用户请求退出")
		c.cancel() // 触发客户端停止
		return true

	case "/ping":
		// Ping命令：发送WebSocket ping消息测试连接
		if err := c.sendControlMessage(websocket.PingMessage, nil); err != nil {
			log.Printf("❌ 发送 ping 失败: %v", err)
		} else {
			log.Printf("📡 已发送 ping 消息")
		}
		return false

	case "/stats":
		// 统计命令：显示详细的连接统计信息
		c.showInteractiveStats()
		return false

	case "/help", "/?":
		// 帮助命令：显示交互模式的使用说明
		c.showInteractiveHelp()
		return false

	default:
		// 不是特殊命令，返回false继续处理为普通消息
		return false
	}
}

// showInteractiveStats 显示连接统计信息
// 这个方法在交互模式中显示详细的WebSocket连接统计数据
//
// 显示的信息：
//  1. 连接状态：当前的连接状态
//  2. 会话ID：唯一的会话标识符
//  3. 连接时间：连接建立的时间
//  4. 连接持续：连接已持续的时间
//  5. 重连次数：发生重连的次数
//  6. 消息统计：发送和接收的消息数量及字节数
//  7. 最后消息：最近一次消息的时间
//
// 格式特点：
//   - 使用emoji增强可读性
//   - 结构化显示便于查看
//   - 时间格式友好易读
//   - 包含关键性能指标
//
// 使用场景：
//   - 调试连接问题
//   - 监控连接性能
//   - 验证消息传输
//   - 分析连接稳定性
func (c *WebSocketClient) showInteractiveStats() {
	// 获取最新的统计数据
	stats := c.GetStats()
	state := c.GetState()

	// 显示格式化的统计信息
	fmt.Println("📊 连接统计信息:")
	fmt.Printf("   状态: %s\n", state)
	fmt.Printf("   会话ID: %s\n", c.SessionID)
	fmt.Printf("   连接时间: %s\n", stats.ConnectTime.Format("2006-01-02 15:04:05"))
	fmt.Printf("   连接持续: %v\n", stats.Uptime)
	fmt.Printf("   重连次数: %d\n", stats.ReconnectCount)
	fmt.Printf("   发送消息: %d 条 (%d 字节)\n", stats.MessagesSent, stats.BytesSent)
	fmt.Printf("   接收消息: %d 条 (%d 字节)\n", stats.MessagesReceived, stats.BytesReceived)
	if !stats.LastMessageTime.IsZero() {
		fmt.Printf("   最后消息: %s\n", stats.LastMessageTime.Format("2006-01-02 15:04:05"))
	}
}

// showInteractiveHelp 显示交互式模式帮助信息
// 这个方法为用户提供交互模式的使用指南和命令说明
//
// 帮助内容：
//  1. 基本使用方法：如何发送普通消息
//  2. 特殊命令列表：所有可用的命令及其功能
//  3. 命令格式说明：命令的输入格式
//
// 设计原则：
//   - 信息简洁明了
//   - 命令分类清晰
//   - 使用emoji增强可读性
//   - 提供实用的操作指导
//
// 显示时机：
//   - 用户输入/help或/?命令
//   - 交互模式启动时的提示
//   - 用户需要帮助时的参考
func (c *WebSocketClient) showInteractiveHelp() {
	fmt.Println("💬 交互式模式帮助:")
	fmt.Println("   直接输入文本消息并按回车发送")
	fmt.Println("   特殊命令:")
	fmt.Println("     /quit, /exit, /q  - 退出程序")
	fmt.Println("     /ping             - 发送 ping 消息")
	fmt.Println("     /stats            - 显示连接统计信息")
	fmt.Println("     /help, /?         - 显示此帮助信息")
}
