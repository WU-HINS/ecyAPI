// bridge.c — libwebp 编码的 C 桥接层。
// 函数指针（WebPWriterFunction）不便从 cgo 直接传递，在此包装成纯数据进出。
#include <stdlib.h>
#include "include/encode.h"

static int mem_write(const uint8_t* data, size_t data_size, const WebPPicture* pic) {
    return WebPMemoryWrite(data, data_size, pic);
}

int webp_bridge_encode_rgba(const uint8_t* rgba, int width, int height, int stride,
                            float quality, uint8_t** out, size_t* out_size) {
    WebPPicture pic;
    WebPConfig cfg;
    WebPMemoryWriter writer;

    if (!WebPPictureInit(&pic)) return 0;
    if (!WebPConfigPreset(&cfg, WEBP_PRESET_PHOTO, quality)) return 0;

    pic.width = width;
    pic.height = height;
    pic.writer = mem_write;
    pic.custom_ptr = &writer;
    WebPMemoryWriterInit(&writer);

    cfg.method = 4; // 压缩率/耗时折中，>4 提升甚微

    if (!WebPPictureImportRGBA(&pic, rgba, stride)) {
        WebPMemoryWriterClear(&writer);
        return 0;
    }
    int ok = WebPEncode(&cfg, &pic);
    WebPPictureFree(&pic);
    if (!ok) {
        WebPMemoryWriterClear(&writer);
        return 0;
    }
    *out = writer.mem;
    *out_size = writer.size;
    return 1;
}
