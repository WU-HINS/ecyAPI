// bridge.h — cgo 侧可见的桥接接口。
#ifndef WEBP_BRIDGE_H
#define WEBP_BRIDGE_H
#include <stdlib.h>
#include <stdint.h>

int webp_bridge_encode_rgba(const uint8_t* rgba, int width, int height, int stride,
                            float quality, uint8_t** out, size_t* out_size);
#endif
