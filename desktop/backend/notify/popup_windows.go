//go:build windows

package notify

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	popupWidth  = 360
	popupHeight = 130
	margin      = 16

	wsPopup        = 0x80000000
	wsVisible      = 0x10000000
	wsClipSiblings = 0x04000000

	wsExTopmost    = 0x00000008
	wsExToolWindow = 0x00000080
	wsExNoActivate = 0x08000000

	spiGetWorkArea = 0x0030

	swpNoActivate = 0x0010
	swpShowWindow = 0x0040
	hwndTopMost   = ^uintptr(0) // -1

	wmCreate    = 0x0001
	wmDestroy   = 0x0002
	wmPaint     = 0x000F
	wmClose     = 0x0010
	wmSetCursor = 0x0020
	wmLButtonUp = 0x0202
	wmTimer     = 0x0113

	dtLeft       = 0x00000000
	dtCenter     = 0x00000001
	dtVCenter    = 0x00000004
	dtSingleLine = 0x00000020
	dtWordBreak  = 0x00000010
	dtNoClip     = 0x00000100

	fwNormal = 400
	fwBold   = 700

	defaultCharset    = 1
	outDefaultPrecis  = 0
	clipDefaultPrecis = 0
	cleartypeQuality  = 5
	defaultPitch      = 0

	transparent = 1

	idcArrow = 32512
	idcHand  = 32649
)

var (
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")

	pGetModuleHandleW      = kernel32.NewProc("GetModuleHandleW")
	pRegisterClassExW      = user32.NewProc("RegisterClassExW")
	pCreateWindowExW       = user32.NewProc("CreateWindowExW")
	pDefWindowProcW        = user32.NewProc("DefWindowProcW")
	pDestroyWindow         = user32.NewProc("DestroyWindow")
	pShowWindow            = user32.NewProc("ShowWindow")
	pUpdateWindow          = user32.NewProc("UpdateWindow")
	pSetWindowPos          = user32.NewProc("SetWindowPos")
	pSystemParametersInfoW = user32.NewProc("SystemParametersInfoW")
	pGetMessageW           = user32.NewProc("GetMessageW")
	pTranslateMessage      = user32.NewProc("TranslateMessage")
	pDispatchMessageW      = user32.NewProc("DispatchMessageW")
	pPostQuitMessage       = user32.NewProc("PostQuitMessage")
	pPostMessageW          = user32.NewProc("PostMessageW")
	pBeginPaint            = user32.NewProc("BeginPaint")
	pEndPaint              = user32.NewProc("EndPaint")
	pFillRect              = user32.NewProc("FillRect")
	pFrameRect             = user32.NewProc("FrameRect")
	pDrawTextW             = user32.NewProc("DrawTextW")
	pSetTimer              = user32.NewProc("SetTimer")
	pKillTimer             = user32.NewProc("KillTimer")
	pSetWindowRgn          = user32.NewProc("SetWindowRgn")
	pLoadCursorW           = user32.NewProc("LoadCursorW")
	pSetCursor             = user32.NewProc("SetCursor")
	pGetCursorPos          = user32.NewProc("GetCursorPos")
	pScreenToClient        = user32.NewProc("ScreenToClient")
	pInvalidateRect        = user32.NewProc("InvalidateRect")

	pCreateSolidBrush   = gdi32.NewProc("CreateSolidBrush")
	pDeleteObject       = gdi32.NewProc("DeleteObject")
	pCreateFontW        = gdi32.NewProc("CreateFontW")
	pSelectObject       = gdi32.NewProc("SelectObject")
	pSetTextColor       = gdi32.NewProc("SetTextColor")
	pSetBkMode          = gdi32.NewProc("SetBkMode")
	pCreateRoundRectRgn = gdi32.NewProc("CreateRoundRectRgn")
	pRoundRect          = gdi32.NewProc("RoundRect")
	pCreatePen          = gdi32.NewProc("CreatePen")

	pShellExecuteW = shell32.NewProc("ShellExecuteW")
)

type rect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type point struct {
	X int32
	Y int32
}

type paintStruct struct {
	Hdc         windows.Handle
	FErase      int32
	RcPaint     rect
	FRestore    int32
	FIncUpdate  int32
	RgbReserved [32]byte
}

type msg struct {
	Hwnd    windows.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

type wndClassExW struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     windows.Handle
	HIcon         windows.Handle
	HCursor       windows.Handle
	HbrBackground windows.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       windows.Handle
}

var (
	popupMu          sync.Mutex
	activePopupHwnd  windows.Handle
	activeCaptchaURL string
	classRegistered  bool
	popupClassName   = syscall.StringToUTF16Ptr("FreeTurnCaptchaPopup")
)

// RGB helper: 0x00BBGGRR
func rgb(r, g, b byte) uint32 {
	return uint32(r) | (uint32(g) << 8) | (uint32(b) << 16)
}

func showCustomCaptchaPopup(captchaURL string) error {
	popupMu.Lock()
	if activePopupHwnd != 0 {
		// Close existing popup window
		pPostMessageW.Call(uintptr(activePopupHwnd), wmClose, 0, 0)
		activePopupHwnd = 0
	}
	popupMu.Unlock()

	createdChan := make(chan error, 1)

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		hInst, _, _ := pGetModuleHandleW.Call(0)

		popupMu.Lock()
		if !classRegistered {
			var cursor windows.Handle
			curRes, _, _ := pLoadCursorW.Call(0, uintptr(idcArrow))
			cursor = windows.Handle(curRes)

			bgBrush, _, _ := pCreateSolidBrush.Call(uintptr(rgb(27, 34, 48))) // #1b2230

			wc := wndClassExW{
				CbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
				Style:         0,
				LpfnWndProc:   syscall.NewCallback(popupWndProc),
				HInstance:     windows.Handle(hInst),
				HCursor:       cursor,
				HbrBackground: windows.Handle(bgBrush),
				LpszClassName: popupClassName,
			}
			res, _, err := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
			if res == 0 && err != windows.ERROR_CLASS_ALREADY_EXISTS {
				popupMu.Unlock()
				createdChan <- fmt.Errorf("register popup class: %w", err)
				return
			}
			classRegistered = true
		}
		popupMu.Unlock()

		var workArea rect
		pSystemParametersInfoW.Call(
			uintptr(spiGetWorkArea),
			0,
			uintptr(unsafe.Pointer(&workArea)),
			0,
		)

		x := workArea.Right - popupWidth - margin
		y := workArea.Bottom - popupHeight - margin

		// If for any reason workArea wasn't populated properly
		if workArea.Right <= 0 || workArea.Bottom <= 0 {
			x = 100
			y = 100
		}

		style := uint32(wsPopup | wsClipSiblings)
		exStyle := uint32(wsExTopmost | wsExToolWindow | wsExNoActivate)

		titlePtr := syscall.StringToUTF16Ptr("FreeTurn Captcha")
		hwndRes, _, err := pCreateWindowExW.Call(
			uintptr(exStyle),
			uintptr(unsafe.Pointer(popupClassName)),
			uintptr(unsafe.Pointer(titlePtr)),
			uintptr(style),
			uintptr(x),
			uintptr(y),
			uintptr(popupWidth),
			uintptr(popupHeight),
			0,
			0,
			hInst,
			0,
		)
		hwnd := windows.Handle(hwndRes)
		if hwnd == 0 {
			createdChan <- fmt.Errorf("create popup window: %w", err)
			return
		}

		popupMu.Lock()
		activePopupHwnd = hwnd
		activeCaptchaURL = captchaURL
		popupMu.Unlock()

		// Rounded corners
		rgnRes, _, _ := pCreateRoundRectRgn.Call(0, 0, uintptr(popupWidth+1), uintptr(popupHeight+1), 12, 12)
		if rgnRes != 0 {
			pSetWindowRgn.Call(uintptr(hwnd), rgnRes, 1)
		}

		// Auto-close timer after 60 seconds
		pSetTimer.Call(uintptr(hwnd), 1, 60000, 0)

		// Show without taking focus
		pSetWindowPos.Call(
			uintptr(hwnd),
			hwndTopMost,
			uintptr(x),
			uintptr(y),
			uintptr(popupWidth),
			uintptr(popupHeight),
			swpShowWindow|swpNoActivate,
		)
		pUpdateWindow.Call(uintptr(hwnd))

		createdChan <- nil

		var m msg
		for {
			res, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(res) <= 0 {
				break
			}
			pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
	}()

	return <-createdChan
}

func popupWndProc(hwnd windows.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	// Button rects:
	// "Решить капчу" button: (20, 84, 160, 114)
	actionBtnRect := rect{Left: 20, Top: 84, Right: 160, Bottom: 114}
	// "✕" button: (330, 8, 352, 30)
	closeBtnRect := rect{Left: 330, Top: 8, Right: 352, Bottom: 30}

	switch msg {
	case wmPaint:
		var ps paintStruct
		hdcRes, _, _ := pBeginPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
		hdc := windows.Handle(hdcRes)
		if hdc != 0 {
			paintPopup(hwnd, hdc, actionBtnRect, closeBtnRect)
			pEndPaint.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&ps)))
		}
		return 0

	case wmSetCursor:
		var pt point
		pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
		pScreenToClient.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&pt)))

		inAction := pt.X >= actionBtnRect.Left && pt.X <= actionBtnRect.Right &&
			pt.Y >= actionBtnRect.Top && pt.Y <= actionBtnRect.Bottom
		inClose := pt.X >= closeBtnRect.Left && pt.X <= closeBtnRect.Right &&
			pt.Y >= closeBtnRect.Top && pt.Y <= closeBtnRect.Bottom

		if inAction || inClose {
			hCursor, _, _ := pLoadCursorW.Call(0, uintptr(idcHand))
			if hCursor != 0 {
				pSetCursor.Call(hCursor)
				return 1
			}
		}
		defCursor, _, _ := pLoadCursorW.Call(0, uintptr(idcArrow))
		if defCursor != 0 {
			pSetCursor.Call(defCursor)
			return 1
		}
		return 0

	case wmLButtonUp:
		x := int32(int16(lParam & 0xffff))
		y := int32(int16((lParam >> 16) & 0xffff))

		if x >= closeBtnRect.Left && x <= closeBtnRect.Right &&
			y >= closeBtnRect.Top && y <= closeBtnRect.Bottom {
			pDestroyWindow.Call(uintptr(hwnd))
			return 0
		}

		if x >= actionBtnRect.Left && x <= actionBtnRect.Right &&
			y >= actionBtnRect.Top && y <= actionBtnRect.Bottom {
			popupMu.Lock()
			url := activeCaptchaURL
			popupMu.Unlock()

			if url != "" {
				urlPtr := syscall.StringToUTF16Ptr(url)
				openPtr := syscall.StringToUTF16Ptr("open")
				pShellExecuteW.Call(0, uintptr(unsafe.Pointer(openPtr)), uintptr(unsafe.Pointer(urlPtr)), 0, 0, 1)
			}
			pDestroyWindow.Call(uintptr(hwnd))
			return 0
		}
		return 0

	case wmTimer:
		if wParam == 1 {
			pKillTimer.Call(uintptr(hwnd), 1)
			pDestroyWindow.Call(uintptr(hwnd))
			return 0
		}

	case wmDestroy:
		pKillTimer.Call(uintptr(hwnd), 1)
		popupMu.Lock()
		if activePopupHwnd == hwnd {
			activePopupHwnd = 0
		}
		popupMu.Unlock()
		pPostQuitMessage.Call(0)
		return 0
	}

	res, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return res
}

func paintPopup(hwnd, hdc windows.Handle, actionBtnRect, closeBtnRect rect) {
	// Set transparent background mode for text
	pSetBkMode.Call(uintptr(hdc), uintptr(transparent))

	// 1. Draw window background (#1b2230)
	bgBrush, _, _ := pCreateSolidBrush.Call(uintptr(rgb(27, 34, 48)))
	fullRect := rect{Left: 0, Top: 0, Right: popupWidth, Bottom: popupHeight}
	pFillRect.Call(uintptr(hdc), uintptr(unsafe.Pointer(&fullRect)), bgBrush)
	pDeleteObject.Call(bgBrush)

	// 2. Draw border (#3a4659)
	borderBrush, _, _ := pCreateSolidBrush.Call(uintptr(rgb(58, 70, 89)))
	pFrameRect.Call(uintptr(hdc), uintptr(unsafe.Pointer(&fullRect)), borderBrush)
	pDeleteObject.Call(borderBrush)

	// Font helper
	segoeUI := syscall.StringToUTF16Ptr("Segoe UI")

	// 3. Draw Title: "FreeTurn: требуется капча VK" (Segoe UI, bold, #ffffff or #e2e8f0)
	titleFont, _, _ := pCreateFontW.Call(
		uintptr(18), // height (~13-14pt)
		0, 0, 0,
		uintptr(fwBold),
		0, 0, 0,
		uintptr(defaultCharset),
		uintptr(outDefaultPrecis),
		uintptr(clipDefaultPrecis),
		uintptr(cleartypeQuality),
		uintptr(defaultPitch),
		uintptr(unsafe.Pointer(segoeUI)),
	)
	oldFont, _, _ := pSelectObject.Call(uintptr(hdc), titleFont)
	pSetTextColor.Call(uintptr(hdc), uintptr(rgb(241, 245, 249))) // #f1f5f9

	titleText := syscall.StringToUTF16("FreeTurn: требуется капча VK")
	titleRect := rect{Left: 20, Top: 14, Right: 320, Bottom: 36}
	pDrawTextW.Call(
		uintptr(hdc),
		uintptr(unsafe.Pointer(&titleText[0])),
		uintptr(len(titleText)-1),
		uintptr(unsafe.Pointer(&titleRect)),
		uintptr(dtLeft|dtSingleLine|dtNoClip),
	)

	// 4. Draw Close button "✕" (#94a3b8)
	closeText := syscall.StringToUTF16("✕")
	pSetTextColor.Call(uintptr(hdc), uintptr(rgb(148, 163, 184))) // #94a3b8
	pDrawTextW.Call(
		uintptr(hdc),
		uintptr(unsafe.Pointer(&closeText[0])),
		uintptr(len(closeText)-1),
		uintptr(unsafe.Pointer(&closeBtnRect)),
		uintptr(dtCenter|dtVCenter|dtSingleLine),
	)

	// 5. Draw Message: "Для продолжения подключения решите капчу в браузере" (Segoe UI 9-10pt, #94a3b8)
	msgFont, _, _ := pCreateFontW.Call(
		uintptr(15), // height (~10pt)
		0, 0, 0,
		uintptr(fwNormal),
		0, 0, 0,
		uintptr(defaultCharset),
		uintptr(outDefaultPrecis),
		uintptr(clipDefaultPrecis),
		uintptr(cleartypeQuality),
		uintptr(defaultPitch),
		uintptr(unsafe.Pointer(segoeUI)),
	)
	pSelectObject.Call(uintptr(hdc), msgFont)
	pSetTextColor.Call(uintptr(hdc), uintptr(rgb(148, 163, 184))) // #94a3b8

	msgText := syscall.StringToUTF16("Для продолжения подключения решите капчу в браузере")
	msgRect := rect{Left: 20, Top: 40, Right: 340, Bottom: 76}
	pDrawTextW.Call(
		uintptr(hdc),
		uintptr(unsafe.Pointer(&msgText[0])),
		uintptr(len(msgText)-1),
		uintptr(unsafe.Pointer(&msgRect)),
		uintptr(dtLeft|dtWordBreak),
	)

	// 6. Draw Action Button "Решить капчу"
	// Background: blue #2563eb / #3b82f6 (rgb: 37, 99, 235)
	btnBrush, _, _ := pCreateSolidBrush.Call(uintptr(rgb(37, 99, 235)))
	btnPen, _, _ := pCreatePen.Call(0, 1, uintptr(rgb(37, 99, 235)))
	oldBrush, _, _ := pSelectObject.Call(uintptr(hdc), btnBrush)
	oldPen, _, _ := pSelectObject.Call(uintptr(hdc), btnPen)

	// Draw rounded rectangle for button
	pRoundRect.Call(
		uintptr(hdc),
		uintptr(actionBtnRect.Left),
		uintptr(actionBtnRect.Top),
		uintptr(actionBtnRect.Right),
		uintptr(actionBtnRect.Bottom),
		6, 6,
	)

	pSelectObject.Call(uintptr(hdc), oldBrush)
	pSelectObject.Call(uintptr(hdc), oldPen)
	pDeleteObject.Call(btnBrush)
	pDeleteObject.Call(btnPen)

	// Button text: white #ffffff, Segoe UI bold
	btnFont, _, _ := pCreateFontW.Call(
		uintptr(15),
		0, 0, 0,
		uintptr(fwBold),
		0, 0, 0,
		uintptr(defaultCharset),
		uintptr(outDefaultPrecis),
		uintptr(clipDefaultPrecis),
		uintptr(cleartypeQuality),
		uintptr(defaultPitch),
		uintptr(unsafe.Pointer(segoeUI)),
	)
	pSelectObject.Call(uintptr(hdc), btnFont)
	pSetTextColor.Call(uintptr(hdc), uintptr(rgb(255, 255, 255)))

	btnText := syscall.StringToUTF16("Решить капчу")
	btnTextRect := actionBtnRect
	pDrawTextW.Call(
		uintptr(hdc),
		uintptr(unsafe.Pointer(&btnText[0])),
		uintptr(len(btnText)-1),
		uintptr(unsafe.Pointer(&btnTextRect)),
		uintptr(dtCenter|dtVCenter|dtSingleLine),
	)

	// Restore GDI state
	pSelectObject.Call(uintptr(hdc), oldFont)
	pDeleteObject.Call(titleFont)
	pDeleteObject.Call(msgFont)
	pDeleteObject.Call(btnFont)
}
