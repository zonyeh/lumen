// Lumen.app: the Lumen window as a macOS app of its own (WebKit), so it
// does not depend on a browser. It starts the app server when needed (the
// `lumen` binary named in Info.plist), restarts it if it stops, and shows
// the interface served on 127.0.0.1. Quitting the window leaves the server
// running for Claude.
import AppKit
import WebKit

final class AppDelegate: NSObject, NSApplicationDelegate, WKNavigationDelegate, WKUIDelegate {
    private var window: NSWindow!
    private var webView: WKWebView!
    private let url: URL
    private let binary: String
    // Only the app's own server is started from here: a window pointed at
    // another address (tests) must never start one on the user's album.
    private let canStart: Bool
    private var starting = false
    private var loaded = false
    private var watchdog: Timer?
    // The server's boot id and interface version: when either changes
    // (a new build started), the page is reloaded from the server so the
    // window never keeps an old interface.
    private var served: String?

    override init() {
        // `open -a Lumen --args -url http://127.0.0.1:5867/` points the window
        // at another server (tests).
        let defaultURL = "http://127.0.0.1:5866/"
        let custom = UserDefaults.standard.string(forKey: "url")
        url = URL(string: custom ?? defaultURL) ?? URL(string: defaultURL)!
        canStart = custom == nil || url.absoluteString == defaultURL
        binary = Bundle.main.object(forInfoDictionaryKey: "LumenBinary") as? String
            ?? Bundle.main.bundleURL.appendingPathComponent("Contents/Resources/lumen").path
        super.init()
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        buildMenu()
        buildWindow()
        ensureServer { [weak self] ok in
            guard let self else { return }
            if ok { self.load() } else { self.showProblem() }
        }
        // Bring the server back if it stops (a new build, a crash).
        watchdog = Timer.scheduledTimer(withTimeInterval: 4, repeats: true) { [weak self] _ in
            self?.ensureServer { _ in }
        }
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { true }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        window.makeKeyAndOrderFront(nil)
        return true
    }

    // MARK: server

    private var port: Int { url.port ?? 5866 }

    private func ping(_ done: @escaping (Bool) -> Void) {
        var req = URLRequest(url: url.appendingPathComponent("api/ping"))
        req.timeoutInterval = 1
        req.cachePolicy = .reloadIgnoringLocalCacheData
        URLSession.shared.dataTask(with: req) { [weak self] data, resp, _ in
            let ok = (resp as? HTTPURLResponse)?.statusCode == 200
            if ok, let self, let data,
               let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] {
                let stamp = "\(obj["boot"] ?? "")-\(obj["ui"] ?? "")"
                DispatchQueue.main.async { self.noteServed(stamp) }
            }
            done(ok)
        }.resume()
    }

    // noteServed reloads the page when a different build answers.
    private func noteServed(_ stamp: String) {
        guard loaded else { served = stamp; return }
        if let old = served, old != stamp {
            NSLog("Lumen: new build (%@): reloading the window", stamp)
            webView.reloadFromOrigin()
        }
        served = stamp
    }

    private func ensureServer(_ done: @escaping (Bool) -> Void) {
        ping { [weak self] alive in
            guard let self else { return }
            if alive {
                DispatchQueue.main.async { done(true) }
                return
            }
            DispatchQueue.main.async {
                if !self.canStart {
                    done(false)
                    return
                }
                if self.starting { return }
                self.starting = true
                DispatchQueue.global().async {
                    let p = Process()
                    p.executableURL = URL(fileURLWithPath: self.binary)
                    p.arguments = ["start", "-port", String(self.port)]
                    var ok = false
                    do {
                        try p.run()
                        p.waitUntilExit()
                        ok = p.terminationStatus == 0
                    } catch {
                        NSLog("Lumen: cannot run %@: %@", self.binary, error.localizedDescription)
                    }
                    DispatchQueue.main.async {
                        self.starting = false
                        done(ok)
                    }
                }
            }
        }
    }

    // MARK: window

    private func buildWindow() {
        let panel = NSColor(srgbRed: 0x17 / 255.0, green: 0x17 / 255.0, blue: 0x17 / 255.0, alpha: 1)
        let screen = NSScreen.main?.visibleFrame ?? NSRect(x: 0, y: 0, width: 1600, height: 1000)
        let size = NSSize(width: min(1600, screen.width - 40), height: min(1000, screen.height - 40))
        window = NSWindow(
            contentRect: NSRect(origin: .zero, size: size),
            styleMask: [.titled, .closable, .miniaturizable, .resizable],
            backing: .buffered, defer: false)
        window.title = "Lumen"
        window.titleVisibility = .hidden
        window.titlebarAppearsTransparent = true
        window.backgroundColor = panel
        window.appearance = NSAppearance(named: .darkAqua)
        window.minSize = NSSize(width: 720, height: 480)
        window.collectionBehavior = [.fullScreenPrimary]
        window.center()
        window.setFrameAutosaveName("Lumen")

        let config = WKWebViewConfiguration()
        config.websiteDataStore = .default()
        config.preferences.setValue(true, forKey: "developerExtrasEnabled")
        config.applicationNameForUserAgent = "Lumen/1"
        webView = WKWebView(frame: window.contentView!.bounds, configuration: config)
        webView.autoresizingMask = [.width, .height]
        webView.navigationDelegate = self
        webView.uiDelegate = self
        webView.allowsMagnification = false
        webView.allowsBackForwardNavigationGestures = false
        webView.setValue(false, forKey: "drawsBackground")
        if #available(macOS 13.3, *) { webView.isInspectable = true }
        window.contentView!.addSubview(webView)
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    private func load() {
        loaded = true
        webView.load(URLRequest(url: url))
    }

    private func showProblem() {
        let html = """
        <html><body style="margin:0;height:100vh;display:grid;place-items:center;background:#121212;color:#e8e8e8;font:13px -apple-system,system-ui">
        <div style="max-width:420px;text-align:center"><p style="font-size:15px;font-weight:600">Lumen could not start</p>
        <p style="color:#9a9a9a">The app server did not answer on \(url.absoluteString). Its log is in ~/Pictures/Lumen/lumen.log.
        Lumen keeps trying and opens as soon as it answers.</p></div></body></html>
        """
        webView.loadHTMLString(html, baseURL: nil)
        // Keep trying in the background.
        Timer.scheduledTimer(withTimeInterval: 3, repeats: true) { [weak self] t in
            guard let self else { t.invalidate(); return }
            self.ping { ok in
                guard ok else { return }
                DispatchQueue.main.async {
                    t.invalidate()
                    self.load()
                }
            }
        }
    }

    // MARK: navigation

    // Pages of the app open here; any other link opens in the default browser.
    func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction,
                 decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        guard let target = action.request.url else { return decisionHandler(.cancel) }
        if target.scheme == "about" || (target.host == url.host && target.port == url.port) {
            decisionHandler(.allow)
        } else {
            NSWorkspace.shared.open(target)
            decisionHandler(.cancel)
        }
    }

    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
        // The server went away while loading: retry once it answers.
        DispatchQueue.main.asyncAfter(deadline: .now() + 1) { [weak self] in
            self?.ensureServer { ok in if ok { self?.load() } }
        }
    }

    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
        webView.reload()
    }

    func webView(_ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration,
                 for action: WKNavigationAction, windowFeatures: WKWindowFeatures) -> WKWebView? {
        if let target = action.request.url { NSWorkspace.shared.open(target) }
        return nil
    }

    func webView(_ webView: WKWebView, runOpenPanelWith parameters: WKOpenPanelParameters,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping ([URL]?) -> Void) {
        let panel = NSOpenPanel()
        panel.allowsMultipleSelection = parameters.allowsMultipleSelection
        panel.canChooseDirectories = parameters.allowsDirectories
        panel.beginSheetModal(for: window) { r in completionHandler(r == .OK ? panel.urls : nil) }
    }

    func webView(_ webView: WKWebView, runJavaScriptAlertPanelWithMessage message: String,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping () -> Void) {
        let a = NSAlert()
        a.messageText = message
        a.beginSheetModal(for: window) { _ in completionHandler() }
    }

    func webView(_ webView: WKWebView, runJavaScriptConfirmPanelWithMessage message: String,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping (Bool) -> Void) {
        let a = NSAlert()
        a.messageText = message
        a.addButton(withTitle: "OK")
        a.addButton(withTitle: "Cancel")
        a.beginSheetModal(for: window) { r in completionHandler(r == .alertFirstButtonReturn) }
    }

    // MARK: menus

    @objc private func reloadPage(_ sender: Any?) {
        if loaded { webView.reload() } else { ensureServer { [weak self] ok in if ok { self?.load() } } }
    }

    private func buildMenu() {
        let main = NSMenu()

        let app = NSMenu()
        app.addItem(withTitle: "About Lumen", action: #selector(NSApplication.orderFrontStandardAboutPanel(_:)), keyEquivalent: "")
        app.addItem(.separator())
        app.addItem(withTitle: "Hide Lumen", action: #selector(NSApplication.hide(_:)), keyEquivalent: "h")
        let others = app.addItem(withTitle: "Hide Others", action: #selector(NSApplication.hideOtherApplications(_:)), keyEquivalent: "h")
        others.keyEquivalentModifierMask = [.command, .option]
        app.addItem(withTitle: "Show All", action: #selector(NSApplication.unhideAllApplications(_:)), keyEquivalent: "")
        app.addItem(.separator())
        app.addItem(withTitle: "Quit Lumen", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
        add(app, title: "Lumen", to: main)

        // Standard editing commands reach text fields; the album's own
        // shortcuts (undo, select all…) are handled by the page first.
        let edit = NSMenu(title: "Edit")
        edit.addItem(withTitle: "Undo", action: Selector(("undo:")), keyEquivalent: "z")
        let redo = edit.addItem(withTitle: "Redo", action: Selector(("redo:")), keyEquivalent: "z")
        redo.keyEquivalentModifierMask = [.command, .shift]
        edit.addItem(.separator())
        edit.addItem(withTitle: "Cut", action: #selector(NSText.cut(_:)), keyEquivalent: "x")
        edit.addItem(withTitle: "Copy", action: #selector(NSText.copy(_:)), keyEquivalent: "c")
        edit.addItem(withTitle: "Paste", action: #selector(NSText.paste(_:)), keyEquivalent: "v")
        edit.addItem(withTitle: "Select All", action: #selector(NSText.selectAll(_:)), keyEquivalent: "a")
        add(edit, title: "Edit", to: main)

        let view = NSMenu(title: "View")
        view.addItem(withTitle: "Reload", action: #selector(reloadPage(_:)), keyEquivalent: "r").target = self
        view.addItem(.separator())
        let fs = view.addItem(withTitle: "Enter Full Screen", action: #selector(NSWindow.toggleFullScreen(_:)), keyEquivalent: "f")
        fs.keyEquivalentModifierMask = [.command, .control]
        add(view, title: "View", to: main)

        let win = NSMenu(title: "Window")
        win.addItem(withTitle: "Minimize", action: #selector(NSWindow.performMiniaturize(_:)), keyEquivalent: "m")
        win.addItem(withTitle: "Zoom", action: #selector(NSWindow.performZoom(_:)), keyEquivalent: "")
        win.addItem(.separator())
        win.addItem(withTitle: "Close", action: #selector(NSWindow.performClose(_:)), keyEquivalent: "w")
        add(win, title: "Window", to: main)
        NSApp.windowsMenu = win

        NSApp.mainMenu = main
    }

    private func add(_ menu: NSMenu, title: String, to main: NSMenu) {
        let item = NSMenuItem(title: title, action: nil, keyEquivalent: "")
        item.submenu = menu
        main.addItem(item)
    }
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.regular)
app.run()
