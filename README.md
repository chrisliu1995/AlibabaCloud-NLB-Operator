# NLB Operator

NLB Operator 是一个用于管理阿里云网络型负载均衡（NLB）实例生命周期的 Kubernetes Operator。它通过自定义资源定义（CRD）的方式，让用户可以使用 Kubernetes 原生方式来创建、更新和删除阿里云 NLB 实例。

## 功能特性

- ✅ **声明式管理**: 通过 Kubernetes CRD 声明式管理 NLB 实例
- ✅ **完整生命周期管理**: 支持 NLB 实例的创建、更新和删除
- ✅ **监听器管理**: 支持配置和管理 NLB 监听器
- ✅ **安全组支持**: 支持配置安全组
- ✅ **保护配置**: 支持删除保护和修改保护
- ✅ **标签管理**: 支持为 NLB 实例添加标签
- ✅ **多可用区支持**: 支持跨可用区部署
- ✅ **状态同步**: 自动同步 NLB 实例状态到 Kubernetes

## 架构设计

本项目参考了 cloud-provider-alibaba-cloud 项目中 Service 控制器的设计，实现了以下组件：

- **CRD 定义**: 定义了 NLB 自定义资源
- **控制器**: 实现了 NLB 资源的 Reconcile 逻辑
- **Provider**: 封装了阿里云 NLB OpenAPI 调用

## 前置要求

- Kubernetes 集群（版本 >= 1.20）
- 阿里云账号及访问密钥（AccessKey ID 和 AccessKey Secret）
- kubectl 命令行工具
- 阿里云 VPC 和 vSwitch

## 快速开始

### 1. 安装 NLB Operator

```bash
# 应用 CRD
kubectl apply -f deploy/crd.yaml

# 应用 RBAC 配置
kubectl apply -f deploy/rbac.yaml

# 更新 deploy/deployment.yaml 中的阿里云凭证
# 然后部署 Operator
kubectl apply -f deploy/deployment.yaml
```

### 2. 配置阿里云凭证

编辑 `deploy/deployment.yaml` 中的 Secret，将 AccessKey ID 和 AccessKey Secret 进行 base64 编码：

```bash
echo -n 'your-access-key-id' | base64
echo -n 'your-access-key-secret' | base64
```

将编码后的值填入 Secret 中。

### 3. 创建 NLB 实例

编辑 `deploy/example-nlb.yaml`，填入您的 VPC、vSwitch、安全组等信息：

```yaml
apiVersion: nlboperator.alibabacloud.com/v1
kind: NLB
metadata:
  name: example-nlb
  namespace: default
spec:
  loadBalancerName: my-nlb-instance
  addressType: Internet
  vpcId: vpc-xxxxx
  zoneMappings:
    - zoneId: cn-hangzhou-h
      vSwitchId: vsw-xxxxx
    - zoneId: cn-hangzhou-i
      vSwitchId: vsw-yyyyy
  listeners:
    - listenerProtocol: TCP
      listenerPort: 80
      serverGroupId: sgp-xxxxx
```

应用配置：

```bash
kubectl apply -f deploy/example-nlb.yaml
```

### 4. 查看 NLB 状态

```bash
# 查看 NLB 列表
kubectl get nlb

# 查看 NLB 详情
kubectl describe nlb example-nlb

# 查看 NLB 状态
kubectl get nlb example-nlb -o yaml
```

### 5. 删除 NLB 实例

```bash
kubectl delete nlb example-nlb
```

## 配置说明

### NLB Spec 字段

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| loadBalancerName | string | 否 | NLB 实例名称 |
| addressType | string | 是 | 网络类型（Internet/Intranet） |
| addressIpVersion | string | 否 | IP 版本（ipv4/DualStack） |
| vpcId | string | 是 | VPC ID |
| zoneMappings | array | 是 | 可用区配置（至少 2 个） |
| resourceGroupId | string | 否 | 资源组 ID |
| securityGroupIds | array | 否 | 安全组 ID 列表 |
| bandwidthPackageId | string | 否 | 共享带宽包 ID |
| deletionProtection | object | 否 | 删除保护配置 |
| modificationProtection | object | 否 | 修改保护配置 |
| tags | array | 否 | 标签列表 |
| listeners | array | 否 | 监听器配置列表 |

### Listener 配置

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| listenerProtocol | string | 是 | 协议类型（TCP/UDP/TCPSSL） |
| listenerPort | int32 | 是 | 监听端口（1-65535） |
| serverGroupId | string | 是 | 后端服务器组 ID |
| listenerDescription | string | 否 | 监听器描述 |
| idleTimeout | int32 | 否 | 空闲超时时间（1-900秒） |
| securityPolicyId | string | 否 | 安全策略 ID（TCPSSL 协议） |
| certificateIds | array | 否 | 证书 ID 列表（TCPSSL 协议） |

## 开发指南

### 构建项目

```bash
# 格式化代码
make fmt

# 代码检查
make vet

# 运行测试
make test

# 构建二进制
make build

# 构建 Docker 镜像
make docker-build

# 推送 Docker 镜像
make docker-push
```

### 本地运行

```bash
# 设置环境变量
export ACCESS_KEY_ID=your-access-key-id
export ACCESS_KEY_SECRET=your-access-key-secret
export REGION_ID=cn-hangzhou

# 运行 Operator
make run
```

## 项目结构

```
nlb-operator/
├── cmd/
│   └── manager/          # 主程序入口
│       └── main.go
├── pkg/
│   ├── apis/             # CRD 定义
│   │   └── nlboperator/
│   │       └── v1/
│   │           ├── nlb_types.go              # NLB CRD 类型定义
│   │           ├── register.go               # Scheme 注册
│   │           └── zz_generated.deepcopy.go  # DeepCopy 方法
│   ├── controller/       # 控制器实现
│   │   └── nlb_controller.go
│   └── provider/         # 阿里云 API 封装
│       └── nlb_client.go
├── deploy/               # 部署文件
│   ├── crd.yaml         # CRD 定义
│   ├── rbac.yaml        # RBAC 配置
│   ├── deployment.yaml  # Operator 部署配置
│   └── example-nlb.yaml # NLB 示例
├── Dockerfile           # Docker 镜像构建文件
├── Makefile            # 构建脚本
├── go.mod              # Go 依赖管理
└── README.md           # 项目文档
```

## API 参考

本项目使用了以下阿里云 OpenAPI：

- `CreateLoadBalancer`: 创建 NLB 实例
- `DeleteLoadBalancer`: 删除 NLB 实例
- `GetLoadBalancerAttribute`: 获取 NLB 实例详情
- `UpdateLoadBalancerProtection`: 更新删除保护配置
- `LoadBalancerJoinSecurityGroup`: 加入安全组
- `CreateListener`: 创建监听器
- `DeleteListener`: 删除监听器
- `GetJobStatus`: 获取异步任务状态

详细的 API 文档请参考：[阿里云 NLB API 文档](https://help.aliyun.com/document_detail/213617.html)

## 注意事项

1. **权限要求**: 运行 Operator 需要阿里云账号具有 NLB 相关的操作权限
2. **资源清理**: 删除 NLB CRD 实例时会自动删除对应的阿里云 NLB 资源
3. **删除保护**: 如果启用了删除保护，删除 NLB 时会自动禁用删除保护再删除
4. **监听器限制**: 每个 NLB 实例最多支持 50 个监听器
5. **可用区要求**: 至少需要配置 2 个可用区

## 故障排查

### 查看 Operator 日志

```bash
kubectl logs -n nlb-operator-system deployment/nlb-operator-controller-manager
```

### 查看 NLB 事件

```bash
kubectl describe nlb example-nlb
```

### 常见问题

1. **NLB 创建失败**: 检查 VPC、vSwitch、安全组配置是否正确
2. **权限不足**: 检查 AccessKey 是否具有 NLB 操作权限
3. **监听器创建失败**: 检查服务器组 ID 是否存在

## 贡献指南

欢迎贡献代码！请遵循以下步骤：

1. Fork 本仓库
2. 创建特性分支 (`git checkout -b feature/AmazingFeature`)
3. 提交更改 (`git commit -m 'Add some AmazingFeature'`)
4. 推送到分支 (`git push origin feature/AmazingFeature`)
5. 开启 Pull Request

## 许可证

本项目采用 Apache 2.0 许可证。

## 联系方式

如有问题或建议，请提交 Issue。

## 参考资料

- [Kubernetes Operator 最佳实践](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/)
- [阿里云 NLB 文档](https://help.aliyun.com/product/143665.html)
- [controller-runtime 文档](https://pkg.go.dev/sigs.k8s.io/controller-runtime)
