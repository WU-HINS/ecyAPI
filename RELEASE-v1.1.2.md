# ecyAPI v1.1.2（紧急修复）

修复一个会导致**进程崩溃重启**的缺陷。

## 问题

```
panic: assignment to entry in nil map
  store.(*fileStore).buildSet → scanDir → scan → refreshLoop
```

**触发条件**：`ECY_ORI_INDEX` 开启（v1.1.0 起默认开启），且服务已运行过一次「扫描 → 释放方向映射」的周期刷新。
表现出来是：启动正常、跑一段时间后随机崩溃重启（CrashLoop）。

## 原因

`dropOriMeta()` 把 `f.oriMeta` 置成了 `nil`，而下一次扫描仍会执行

```go
f.oriMeta[rel] = meta   // 对 nil map 赋值 → panic
```

v1.1.0/v1.1.1 的单元测试只做单次扫描，没有覆盖「释放后再扫描」这条路径。

## 修复

1. `dropOriMeta()` 改为置空 map（不置 nil）
2. `buildSet` 写入前兜底初始化（双保险）
3. 新增回归测试 `TestScanAfterDropOriMeta`：释放后连续两次扫描不 panic，计数正确

## 立即缓解（暂不升级的话）

```bash
ECY_ORI_INDEX=0   # 关闭方向索引，绕开该路径；代价是方向映射重新占用约 126B/条
```

## 升级

替换二进制即可，无配置变更、无索引格式变更。