package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const gridMaxBytes = 16 << 20
const gridMaxPixels = 16 << 20
const gridMaxOutputBytes = 64 << 20
const gridMarginX, gridMarginY = 24, 12

type ImageGridRequest struct {
	InputPath  string `json:"input_path"`
	OutputPath string `json:"output_path"`
	Spacing    int    `json:"spacing"`
}

type ImageGridResult struct {
	InputPath    string `json:"input_path"`
	OutputPath   string `json:"output_path"`
	InputSHA256  string `json:"input_sha256"`
	OutputSHA256 string `json:"output_sha256"`
	InputWidth   int    `json:"input_width"`
	InputHeight  int    `json:"input_height"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	Spacing      int    `json:"spacing"`
	Columns      int    `json:"columns"`
	Rows         int    `json:"rows"`
	OriginX      int    `json:"origin_x"`
	OriginY      int    `json:"origin_y"`
	InputFormat  string `json:"input_format"`
	Orientation  string `json:"orientation"`
}

// CreateImageGrid overlays red lines and numbered row/column margins on a separate PNG.
// JPEG orientation is stored-pixel order: EXIF rotation is not applied or preserved.
func CreateImageGrid(ctx context.Context, projectRoot string, input ImageGridRequest) (ImageGridResult, error) {
	var result ImageGridResult
	if !localPath(input.InputPath) || !localPath(input.OutputPath) || input.InputPath == input.OutputPath {
		return result, errors.New("select distinct relative input and output paths")
	}
	if input.Spacing < 16 || input.Spacing > 8192 {
		return result, errors.New("spacing must be an integer from 16 through 8192 pixels")
	}
	root, err := openLocalRoot(projectRoot)
	if err != nil {
		return result, err
	}
	defer root.Close()
	output, err := localStat(root, input.OutputPath, true)
	if err != nil {
		return result, err
	}
	if output != nil {
		return result, errors.New("output already exists; select a new PNG path")
	}
	data, err := localRead(ctx, root, input.InputPath, gridMaxBytes)
	if err != nil {
		return result, err
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") {
		return result, errors.New("input must be a decodable PNG or JPEG")
	}
	w, h := config.Width, config.Height
	if w <= 0 || h <= 0 || w > 8192 || h > 8192 || int64(w)*int64(h) > gridMaxPixels {
		return result, errors.New("image exceeds 8192 per dimension or 16 megapixels")
	}
	cols, rows := (w-1)/input.Spacing+1, (h-1)/input.Spacing+1
	if cols+rows > 1024 {
		return result, errors.New("grid exceeds 1024 total numbered lines; increase spacing")
	}
	outW, outH := w+2*gridMarginX, h+2*gridMarginY
	if outW > 8240 || outH > 8216 || int64(outW)*int64(outH) > 17<<20 {
		return result, errors.New("grid output dimensions exceed the supported limit")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	source, decodedFormat, err := image.Decode(bytes.NewReader(data))
	if err != nil || decodedFormat != format {
		return result, errors.New("image decoding failed")
	}
	if source.Bounds().Dx() != w || source.Bounds().Dy() != h {
		return result, errors.New("image dimensions changed during decoding")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	canvas := image.NewRGBA(image.Rect(0, 0, outW, outH))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	imageRect := image.Rect(gridMarginX, gridMarginY, gridMarginX+w, gridMarginY+h)
	draw.Draw(canvas, imageRect, source, source.Bounds().Min, draw.Src)
	line := image.NewUniform(color.RGBA{R: 220, G: 30, B: 30, A: 255})
	for col := 0; col < cols; col++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		x := gridMarginX + col*input.Spacing
		draw.Draw(canvas, image.Rect(x, gridMarginY, x+1, gridMarginY+h), line, image.Point{}, draw.Src)
		label := strconv.Itoa(col + 1)
		gridDigits(canvas, x-(len(label)*4-1)/2, 2, label)
	}
	for row := 0; row < rows; row++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		y := gridMarginY + row*input.Spacing
		draw.Draw(canvas, image.Rect(gridMarginX, y, gridMarginX+w, y+1), line, image.Point{}, draw.Src)
		label := strconv.Itoa(row + 1)
		gridDigits(canvas, gridMarginX-4-(len(label)*4-1), y-2, label)
	}
	var encoded bytes.Buffer
	if err := png.Encode(&gridOutput{ctx: ctx, target: &encoded}, canvas); err != nil {
		return result, errors.New("PNG encoding cancelled or exceeded its output limit")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := localCreate(ctx, root, input.OutputPath, encoded.Bytes()); err != nil {
		return result, err
	}
	result = ImageGridResult{InputPath: input.InputPath, OutputPath: input.OutputPath, InputSHA256: localHash(data), OutputSHA256: localHash(encoded.Bytes()), InputWidth: w, InputHeight: h, Width: outW, Height: outH, Spacing: input.Spacing, Columns: cols, Rows: rows, OriginX: gridMarginX, OriginY: gridMarginY, InputFormat: format, Orientation: "stored_pixels_no_exif_rotation"}
	return result, nil
}

type gridOutput struct {
	ctx    context.Context
	target *bytes.Buffer
}

func (w *gridOutput) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > gridMaxOutputBytes-w.target.Len() {
		return 0, errors.New("PNG output limit exceeded")
	}
	return w.target.Write(p)
}

// Three by five digits keep the pure-Go helper independent of font/rendering packages.
var gridDigitBits = [10][5]byte{{7, 5, 5, 5, 7}, {2, 6, 2, 2, 7}, {7, 1, 7, 4, 7}, {7, 1, 7, 1, 7}, {5, 5, 7, 1, 1}, {7, 4, 7, 1, 7}, {7, 4, 7, 5, 7}, {7, 1, 1, 1, 1}, {7, 5, 7, 5, 7}, {7, 5, 7, 1, 7}}

func gridDigits(dst *image.RGBA, x, y int, label string) {
	for i, digit := range []byte(label) {
		for dy, bits := range gridDigitBits[digit-'0'] {
			for dx := 0; dx < 3; dx++ {
				if bits&(1<<uint(2-dx)) != 0 {
					dst.Set(x+i*4+dx, y+dy, color.Black)
				}
			}
		}
	}
}

func ImageGridTool() *mcp.Tool {
	return &mcp.Tool{Name: "image_grid", Title: "Пронумерованная сетка изображения", Description: "Наложить красную сетку с номерами строк/столбцов на локальный PNG/JPEG, записать отдельный новый PNG. spacing:16–8192 пикселей; до16MiB/16M пикселей,8192 на сторону и1024 линий. Оригинал сохраняется. Номера находятся в белых полях: по24px слева/справа,12px сверху/снизу; исходник начинается в(24,12). JPEG читается без поворота EXIF, метаданные не переносятся. Нужен выбранный project-root/dump, родитель output должен существовать. Не преобразует изображение в MXL и ничего не загружает в сеть.", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"input_path":{"type":"string"},"output_path":{"type":"string"},"spacing":{"type":"integer","minimum":16,"maximum":8192}},"required":["input_path","output_path","spacing"]}`)}
}

func NewImageGridHandler(root string) mcp.ToolHandler {
	return WithToolErrors("Сетка изображения не получена", func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input ImageGridRequest
		if err := localDecode(req.Params.Arguments, &input); err != nil {
			return nil, InvalidParams(argumentDecodeError(err))
		}
		result, err := CreateImageGrid(ctx, root, input)
		if err != nil {
			return nil, err
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		return textResult(string(data)), nil
	})
}
