//go:build windows

package tray

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	trayClassName = "FreeTurnTrayClass"
	wmUser        = 0x0400
	wmTrayIcon    = wmUser + 1
	wmCloseTray   = wmUser + 2

	cmdOpen = 1001
	cmdQuit = 1002

	// Shell_NotifyIcon message constants
	nimAdd    = 0x00000000
	nimModify = 0x00000001
	nimDelete = 0x00000002

	// NOTIFYICONDATA flags
	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004

	// Window messages
	wmDestroy       = 0x0002
	wmCommand       = 0x0111
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmContextMenu   = 0x007B
	wmNull          = 0x0000

	// Menu flags
	mfString       = 0x00000000
	mfSeparator    = 0x00000800
	tpmRightButton = 0x0002

	// LoadIcon constants
	idiApplication = 32512
)

var (
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	user32   = windows.NewLazySystemDLL("user32.dll")

	pGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")

	pShell_NotifyIconW = shell32.NewProc("Shell_NotifyIconW")

	pRegisterClassExW    = user32.NewProc("RegisterClassExW")
	pCreateWindowExW     = user32.NewProc("CreateWindowExW")
	pDefWindowProcW      = user32.NewProc("DefWindowProcW")
	pDestroyWindow       = user32.NewProc("DestroyWindow")
	pGetMessageW         = user32.NewProc("GetMessageW")
	pTranslateMessage    = user32.NewProc("TranslateMessage")
	pDispatchMessageW    = user32.NewProc("DispatchMessageW")
	pPostQuitMessage     = user32.NewProc("PostQuitMessage")
	pPostMessageW        = user32.NewProc("PostMessageW")
	pCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	pAppendMenuW         = user32.NewProc("AppendMenuW")
	pTrackPopupMenuEx    = user32.NewProc("TrackPopupMenuEx")
	pDestroyMenu         = user32.NewProc("DestroyMenu")
	pGetCursorPos        = user32.NewProc("GetCursorPos")
	pSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	pLoadIconW           = user32.NewProc("LoadIconW")
	pDestroyIcon         = user32.NewProc("DestroyIcon")
	pExtractIconExW      = shell32.NewProc("ExtractIconExW")
)

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     windows.Handle
	hIcon         windows.Handle
	hCursor       windows.Handle
	hbrBackground windows.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       windows.Handle
}

type point struct {
	x int32
	y int32
}

type notifyIconDataW struct {
	cbSize            uint32
	hWnd              windows.Handle
	uID               uint32
	uFlags            uint32
	uCallbackMessage  uint32
	hIcon             windows.Handle
	szTip             [128]uint16
	dwState           uint32
	dwStateMask       uint32
	szInfo            [256]uint16
	uTimeoutOrVersion uint32
	szInfoTitle       [64]uint16
	dwInfoFlags       uint32
	guidItem          windows.GUID
	hBalloonIcon      windows.Handle
}

// Tray controls the Windows system tray lifecycle.
type Tray struct {
	hwnd      windows.Handle
	onShow    func()
	onQuit    func()
	closed    chan struct{}
	closeOnce sync.Once
}

var (
	activeTrayMu sync.RWMutex
	activeTrays  = make(map[windows.Handle]*Tray)
)

func wndProc(hwnd windows.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	activeTrayMu.RLock()
	t := activeTrays[hwnd]
	activeTrayMu.RUnlock()

	switch msg {
	case wmTrayIcon:
		switch lParam {
		case wmLButtonUp, wmLButtonDblClk:
			if t != nil && t.onShow != nil {
				go t.onShow()
			}
			return 0
		case wmRButtonUp, wmContextMenu:
			if t != nil {
				t.showMenu()
			}
			return 0
		}

	case wmCommand:
		cmd := uint32(wParam) & 0xFFFF
		switch cmd {
		case cmdOpen:
			if t != nil && t.onShow != nil {
				go t.onShow()
			}
			return 0
		case cmdQuit:
			if t != nil && t.onQuit != nil {
				go t.onQuit()
			}
			return 0
		}

	case wmCloseTray:
		pDestroyWindow.Call(uintptr(hwnd))
		return 0

	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	}

	ret, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return ret
}

func (t *Tray) showMenu() {
	var pt point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))

	hMenu, _, _ := pCreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}
	defer pDestroyMenu.Call(hMenu)

	openTitlePtr, _ := windows.UTF16PtrFromString("Открыть FreeTurn")
	quitTitlePtr, _ := windows.UTF16PtrFromString("Выход")

	pAppendMenuW.Call(hMenu, uintptr(mfString), uintptr(cmdOpen), uintptr(unsafe.Pointer(openTitlePtr)))
	pAppendMenuW.Call(hMenu, uintptr(mfSeparator), 0, 0)
	pAppendMenuW.Call(hMenu, uintptr(mfString), uintptr(cmdQuit), uintptr(unsafe.Pointer(quitTitlePtr)))

	pSetForegroundWindow.Call(uintptr(t.hwnd))
	pTrackPopupMenuEx.Call(hMenu, uintptr(tpmRightButton), uintptr(pt.x), uintptr(pt.y), uintptr(t.hwnd), 0)
	pPostMessageW.Call(uintptr(t.hwnd), uintptr(wmNull), 0, 0)
}

func getAppIcon() (windows.Handle, bool) {
	// Try to get icon from current executable
	var exePath [windows.MAX_PATH]uint16
	n, err := windows.GetModuleFileName(0, &exePath[0], uint32(len(exePath)))
	if err == nil && n > 0 {
		var hIcon windows.Handle
		ret, _, _ := pExtractIconExW.Call(
			uintptr(unsafe.Pointer(&exePath[0])),
			0,
			0,
			uintptr(unsafe.Pointer(&hIcon)),
			1,
		)
		if ret > 0 && hIcon != 0 {
			return hIcon, true
		}
	}

	// Fallback to standard application icon
	hIcon, _, _ := pLoadIconW.Call(0, uintptr(idiApplication))
	return windows.Handle(hIcon), false
}

// Start creates and initializes the tray icon and message loop.
func Start(title string, onShow func(), onQuit func()) (*Tray, error) {
	if title == "" {
		title = "FreeTurn Desktop"
	}

	ready := make(chan error, 1)
	tray := &Tray{
		onShow: onShow,
		onQuit: onQuit,
		closed: make(chan struct{}),
	}

	go func() {
		// Lock OS thread for Win32 message loop
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		hInst, _, _ := pGetModuleHandleW.Call(0)

		hInstance := windows.Handle(hInst)

		classNamePtr, err := windows.UTF16PtrFromString(trayClassName)
		if err != nil {
			ready <- fmt.Errorf("class name conversion: %w", err)
			return
		}

		wndProcPtr := syscall.NewCallback(wndProc)

		wc := wndClassExW{
			cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
			lpfnWndProc:   wndProcPtr,
			hInstance:     hInstance,
			lpszClassName: classNamePtr,
		}

		pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		// RegisterClassExW may fail if class is already registered in process, which is fine

		hwnd, _, callErr := pCreateWindowExW.Call(
			0,
			uintptr(unsafe.Pointer(classNamePtr)),
			0,
			0,
			0, 0, 0, 0,
			0,
			0,
			uintptr(hInstance),
			0,
		)

		if hwnd == 0 {
			ready <- fmt.Errorf("CreateWindowExW failed: %v", callErr)
			return
		}

		tray.hwnd = windows.Handle(hwnd)

		activeTrayMu.Lock()
		activeTrays[tray.hwnd] = tray
		activeTrayMu.Unlock()

		hIcon, shouldDestroyIcon := getAppIcon()

		nid := notifyIconDataW{
			hWnd:             tray.hwnd,
			uID:              1,
			uFlags:           nifIcon | nifMessage | nifTip,
			uCallbackMessage: wmTrayIcon,
			hIcon:            hIcon,
		}
		nid.cbSize = uint32(unsafe.Sizeof(nid))

		tipChars, err := windows.UTF16FromString(title)
		if err == nil {
			copy(nid.szTip[:], tipChars)
		}

		ret, _, _ := pShell_NotifyIconW.Call(uintptr(nimAdd), uintptr(unsafe.Pointer(&nid)))
		if ret == 0 {
			if shouldDestroyIcon && hIcon != 0 {
				pDestroyIcon.Call(uintptr(hIcon))
			}
			pDestroyWindow.Call(uintptr(tray.hwnd))
			activeTrayMu.Lock()
			delete(activeTrays, tray.hwnd)
			activeTrayMu.Unlock()
			ready <- errors.New("Shell_NotifyIconW (NIM_ADD) failed")
			return
		}

		// Tray is successfully created
		ready <- nil

		// Message loop
		var msg struct {
			hwnd     windows.Handle
			message  uint32
			wParam   uintptr
			lParam   uintptr
			time     uint32
			pt       point
			lPrivate uint32
		}

		for {
			res, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if int32(res) <= 0 {
				break
			}
			pTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			pDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
		}

		// Cleanup notify icon
		delNid := notifyIconDataW{
			hWnd: tray.hwnd,
			uID:  1,
		}
		delNid.cbSize = uint32(unsafe.Sizeof(delNid))
		pShell_NotifyIconW.Call(uintptr(nimDelete), uintptr(unsafe.Pointer(&delNid)))

		if shouldDestroyIcon && hIcon != 0 {
			pDestroyIcon.Call(uintptr(hIcon))
		}

		activeTrayMu.Lock()
		delete(activeTrays, tray.hwnd)
		activeTrayMu.Unlock()

		close(tray.closed)
	}()

	if err := <-ready; err != nil {
		return nil, err
	}

	return tray, nil
}

// Close removes the tray icon and terminates the tray window and message loop.
func (t *Tray) Close() {
	t.closeOnce.Do(func() {
		if t.hwnd != 0 {
			pPostMessageW.Call(uintptr(t.hwnd), uintptr(wmCloseTray), 0, 0)
			select {
			case <-t.closed:
			case <-time.After(2 * time.Second):
			}
		}
	})
}
