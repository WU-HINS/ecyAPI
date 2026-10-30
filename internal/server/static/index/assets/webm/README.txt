看板娘的动画（99 个 .webm，约 50 MB）体积过大，不随仓库分发。
运行 make assets（或 go run ./tools/fetchassets）可把它们下载到运行目录的 static/assets/webm/，
服务器会优先从磁盘目录读取，缺失时回落到仓库内置的静态文件。
