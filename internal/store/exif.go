package store

// exif.go —— 读 JPEG 的 EXIF Orientation，修正「像素横但显示竖」的手机照片。
//
// 只做一件事：从 SOI 之后扫描 APP1（Exif），解析 TIFF IFD0 的 0x0112 标签。
// 返回 1..8，读不到返回 0。不解其它 EXIF，不引入依赖。

import "encoding/binary"

// jpegEXIFOrientation 从 JPEG 字节流里取 EXIF Orientation（1..8），读不到返回 0。
func jpegEXIFOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 0
	}
	i := 2
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return 0
		}
		marker := b[i+1]
		if marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			i += 2
			continue
		}
		if marker == 0xDA { // Start of Scan，后面是压缩数据
			return 0
		}
		segLen := int(binary.BigEndian.Uint16(b[i+2:]))
		if segLen < 2 || i+2+segLen > len(b) {
			return 0
		}
		if marker == 0xE1 {
			payload := b[i+4 : i+2+segLen]
			if len(payload) > 6 && string(payload[:4]) == "Exif" {
				return parseTIFFOrientation(payload[6:])
			}
		}
		i += 2 + segLen
	}
	return 0
}

// parseTIFFOrientation 解析 TIFF 结构里的 Orientation 标签。
func parseTIFFOrientation(t []byte) int {
	if len(t) < 8 {
		return 0
	}
	var order binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 0
	}
	if order.Uint16(t[2:4]) != 42 {
		return 0
	}
	ifd := int(order.Uint32(t[4:8]))
	if ifd+2 > len(t) {
		return 0
	}
	n := int(order.Uint16(t[ifd : ifd+2]))
	base := ifd + 2
	for k := 0; k < n; k++ {
		e := base + k*12
		if e+12 > len(t) {
			return 0
		}
		tag := order.Uint16(t[e : e+2])
		if tag != 0x0112 {
			continue
		}
		typ := order.Uint16(t[e+2 : e+4])
		if typ != 3 { // SHORT
			return 0
		}
		v := int(order.Uint16(t[e+8 : e+10]))
		if v >= 1 && v <= 8 {
			return v
		}
		return 0
	}
	return 0
}

// swapForEXIF 判断该 EXIF 方向是否需要交换宽高（5~8 表示旋转 90°/270°）。
func swapForEXIF(o int) bool { return o >= 5 && o <= 8 }

// JPEGEXIFOrientation 对外暴露（服务端代理路径判方向时复用）。
func JPEGEXIFOrientation(b []byte) int { return jpegEXIFOrientation(b) }

// SwapForEXIF 对外暴露：5~8 表示需要交换宽高。
func SwapForEXIF(o int) bool { return swapForEXIF(o) }
