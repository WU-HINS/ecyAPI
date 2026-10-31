"""生成压测报告：读 bench/results.jsonl，输出 bench/bench.md 并写 GITHUB_OUTPUT。"""

import json
import os
import sys

NAMES = {
    "/json": "`/json?c000`",
    "/c000": "`/c000`（302）",
    "/@img": "`/@img`（加权选分类）",
    "/img": "`/img` 图库 gzip",
}


def label_for(url: str) -> str:
    if "json?" in url:
        return NAMES["/json"]
    path = url.split("/", 3)[-1] if "://" in url else url
    for key, name in NAMES.items():
        if path == key.lstrip("/") or url.endswith(key):
            return name
    return url


def main() -> int:
    rows, mem = [], None
    with open("bench/results.jsonl", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            d = json.loads(line)
            if d.get("rss_mb"):
                mem = d
                continue
            rows.append(d)

    md = ["### 压测结果（本版本 CI 实测）", "",
          "数据集 1000 分类 / 50 000 张，限额 512MB，各跑 6 秒（同机压测）：", "",
          "| 接口 | 并发 | QPS | p50 | p99 | 失败 |",
          "| --- | --- | --- | --- | --- | --- |"]
    for r in rows:
        md.append("| %s | %d | %.0f | %.1f ms | %.1f ms | %d |" % (
            label_for(r["url"]), r["workers"], r["qps"],
            r["p50_ms"], r["p99_ms"], r["failed"]))
    if mem:
        md += ["", "峰值 RSS **%s MB**（空闲 %s MB）。" % (mem["peak_mb"], mem["rss_mb"])]
    md += ["", "> 由压测工作流（.github/workflows/bench.yml）自动生成；同机压测含客户端开销，仅供参考。", ""]
    text = "\n".join(md)
    with open("bench/bench.md", "w", encoding="utf-8") as f:
        f.write(text)
    out = os.environ.get("GITHUB_OUTPUT")
    if out:
        with open(out, "a", encoding="utf-8") as f:
            f.write("summary<<BENCH_EOF\n" + text + "BENCH_EOF\n")
    print(text)
    return 0


if __name__ == "__main__":
    sys.exit(main())