#pragma once

#include <juce_core/juce_core.h>
#include <juce_events/juce_events.h>
#include <atomic>
#include <functional>

/** Non-ASCII string literals: the sources are UTF-8. */
#define RU(text) juce::String (juce::CharPointer_UTF8 (text))

/** The folder binding this plugin instance asks the helper to keep. */
struct WatchSpec
{
    juce::String id, localPath, accountId, remoteFolder;
    bool recursive = true;
    bool overwrite = false;   // false: previous versions are kept in the cloud

    bool operator== (const WatchSpec& o) const
    {
        return id == o.id && localPath == o.localPath && accountId == o.accountId
            && remoteFolder == o.remoteFolder && recursive == o.recursive && overwrite == o.overwrite;
    }
    bool operator!= (const WatchSpec& o) const { return ! (*this == o); }
};

/** What the helper last told us; a plain copy handed to the interface. */
struct Snapshot
{
    bool connected = false;
    juce::String problem;          // why there is no connection, if known
    juce::String helperVersion;
    bool paused = false;
    bool secretsInFile = false;

    struct Provider { juce::String id, name; bool configured = false, manualToken = false; };
    struct Account  { juce::String id, provider, name, status; juce::int64 total = 0, free = -1; };
    struct Failure  { juce::String file, error; };
    struct Watch
    {
        juce::String id, localPath, accountId, remoteFolder, lastError;
        bool available = true;
        int pending = 0, failed = 0, uploaded = 0, known = 0;
        juce::Array<Failure> failures;
    };
    struct Upload { juce::String watchId, file; juce::int64 sent = 0, size = 0; };
    struct Event  { juce::int64 time = 0; juce::String watchId, file, kind, detail; };
    struct Login  { juce::String id, provider, url, userCode, state, error; };

    juce::Array<Provider> providers;
    juce::Array<Account> accounts;
    juce::Array<Watch> watches;
    juce::Array<Upload> uploads;
    juce::Array<Event> recent;
    Login login;
    juce::String notice;           // result of the last command, to show once

    const Account* account (const juce::String& id) const
    {
        for (auto& a : accounts) if (a.id == id) return &a;
        return nullptr;
    }
    const Watch* watch (const juce::String& id) const
    {
        for (auto& w : watches) if (w.id == id) return &w;
        return nullptr;
    }
};

/**
    Talks to the background helper from its own thread.

    Nothing here runs on the audio thread, and the message thread only copies
    data under a short lock: the DAW never waits for the helper, the network
    or the disk. The helper is started from the plugin bundle when it is not
    running; it is never stopped from here, because other plugin instances
    share it and it must finish uploading after the DAW has gone.
*/
class HelperClient : private juce::Thread,
                     private juce::AsyncUpdater
{
public:
    HelperClient();
    ~HelperClient() override;

    /** Starts the background thread. Until then nothing is launched or
        contacted, so a DAW scanning its plugins does not wake the helper. */
    void start();

    Snapshot getSnapshot() const;
    WatchSpec getWatch() const;

    /** Replaces the binding (from project state or from the interface). */
    void setWatch (const WatchSpec&);

    /** Polls faster while the window is open. */
    void setInterfaceOpen (bool);

    void startLogin (const juce::String& provider);
    void cancelLogin();
    void submitToken (const juce::String& token);
    void logout (const juce::String& accountId);
    void uploadExisting();
    void retry (const juce::String& file);
    void setPaused (bool);

    /** Called on the message thread after the snapshot changed. */
    std::function<void()> onChange;

    /** Where the helper keeps its files (same rule as the helper itself). */
    static juce::File dataDirectory();
    /** The helper executable shipped inside this plugin's bundle. */
    static juce::File bundledHelper();
    /** True when version a is older than version b ("0.2.0" < "0.10.0"). */
    static bool olderThan (const juce::String& a, const juce::String& b);

private:
    struct Command
    {
        Command (juce::String t, juce::String first = {}, juce::String second = {}, bool f = false)
            : type (std::move (t)), a (std::move (first)), b (std::move (second)), flag (f) {}
        juce::String type, a, b;
        bool flag;
    };
    struct Reply   { int status = 0; juce::String body; juce::var json; bool ok() const { return status >= 200 && status < 300; } };

    void run() override;
    void handleAsyncUpdate() override;
    bool pause (int milliseconds);

    bool connect();
    void launchHelper();
    Reply call (const juce::String& method, const juce::String& path, const juce::var& body = {});
    void poll();
    void ensureWatch();
    void execute (const Command&);
    void push (Command);
    void publish (const Snapshot&);
    static Snapshot parseStatus (const juce::var&);

    const juce::String clientId;
    std::atomic<bool> started { false };

    // Owned by the worker thread.
    juce::String baseUrl, secret;
    juce::int64 lastLaunch = 0;
    bool watchDirty = true;
    juce::String openedLoginId;

    mutable juce::CriticalSection lock;   // guards everything below
    Snapshot snapshot;
    WatchSpec spec;
    juce::Array<Command> commands;
    juce::String urlToOpen;
    bool interfaceOpen = false;

    JUCE_DECLARE_NON_COPYABLE_WITH_LEAK_DETECTOR (HelperClient)
};
