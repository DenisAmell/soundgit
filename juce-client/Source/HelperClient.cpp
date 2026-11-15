#include "HelperClient.h"

#ifndef AUDIO_BACKUPER_HELPER_VERSION
 #define AUDIO_BACKUPER_HELPER_VERSION "0.0.0"
#endif

namespace
{
    juce::String str (const juce::var& v, const char* key)   { return v.getProperty (key, {}).toString(); }
    bool flag (const juce::var& v, const char* key)          { return (bool) v.getProperty (key, false); }
    int integer (const juce::var& v, const char* key)        { return (int) v.getProperty (key, 0); }
    juce::int64 big (const juce::var& v, const char* key)    { return (juce::int64) v.getProperty (key, 0); }

    juce::var object (std::initializer_list<std::pair<const char*, juce::var>> fields)
    {
        auto* o = new juce::DynamicObject();
        for (auto& f : fields)
            o->setProperty (f.first, f.second);
        return juce::var (o);
    }
}

HelperClient::HelperClient()
    : juce::Thread ("Audio Backuper helper client"),
      clientId (juce::Uuid().toString())
{
}

void HelperClient::start()
{
    if (! started.exchange (true))
        startThread (juce::Thread::Priority::low);
}

HelperClient::~HelperClient()
{
    cancelPendingUpdate();
    signalThreadShouldExit();
    notify();
    // Requests go to this machine with sub-second timeouts, so this returns
    // promptly; the helper itself keeps running.
    stopThread (3000);
}

juce::File HelperClient::dataDirectory()
{
    auto override = juce::SystemStats::getEnvironmentVariable ("AUDIO_BACKUPER_DATA_DIR", {});
    if (override.isNotEmpty())
        return juce::File (override);

   #if JUCE_MAC
    return juce::File::getSpecialLocation (juce::File::userHomeDirectory)
             .getChildFile ("Library/Application Support/AudioBackuper");
   #elif JUCE_WINDOWS
    return juce::File::getSpecialLocation (juce::File::userApplicationDataDirectory)
             .getChildFile ("AudioBackuper");
   #else
    auto xdg = juce::SystemStats::getEnvironmentVariable ("XDG_CONFIG_HOME", {});
    auto base = xdg.isNotEmpty() ? juce::File (xdg)
                                 : juce::File::getSpecialLocation (juce::File::userHomeDirectory).getChildFile (".config");
    return base.getChildFile ("audio-backuper");
   #endif
}

juce::File HelperClient::bundledHelper()
{
    auto override = juce::SystemStats::getEnvironmentVariable ("AUDIO_BACKUPER_HELPER", {});
    if (override.isNotEmpty())
        return juce::File (override);

   #if JUCE_MAC
    // currentApplicationFile is the plugin bundle itself (.vst3, .component, .app).
    return juce::File::getSpecialLocation (juce::File::currentApplicationFile)
             .getChildFile ("Contents/Resources/audio-backuper-helper");
   #elif JUCE_WINDOWS
    // <name>.vst3/Contents/x86_64-win/<name>.vst3  →  <name>.vst3/Contents/Resources
    return juce::File::getSpecialLocation (juce::File::currentExecutableFile)
             .getParentDirectory().getParentDirectory()
             .getChildFile ("Resources/audio-backuper-helper.exe");
   #else
    return juce::File::getSpecialLocation (juce::File::currentExecutableFile)
             .getParentDirectory().getParentDirectory()
             .getChildFile ("Resources/audio-backuper-helper");
   #endif
}

bool HelperClient::olderThan (const juce::String& a, const juce::String& b)
{
    auto pa = juce::StringArray::fromTokens (a, ".", {});
    auto pb = juce::StringArray::fromTokens (b, ".", {});
    for (int i = 0; i < juce::jmax (pa.size(), pb.size()); ++i)
    {
        auto x = i < pa.size() ? pa[i].getIntValue() : 0;
        auto y = i < pb.size() ? pb[i].getIntValue() : 0;
        if (x != y)
            return x < y;
    }
    return false;
}

Snapshot HelperClient::getSnapshot() const
{
    const juce::ScopedLock sl (lock);
    return snapshot;
}

WatchSpec HelperClient::getWatch() const
{
    const juce::ScopedLock sl (lock);
    return spec;
}

void HelperClient::setWatch (const WatchSpec& w)
{
    {
        const juce::ScopedLock sl (lock);
        if (spec == w)
            return;
        spec = w;
        commands.add (Command ("ensure"));
    }
    notify();
}

void HelperClient::setInterfaceOpen (bool open)
{
    {
        const juce::ScopedLock sl (lock);
        interfaceOpen = open;
    }
    notify();
}

void HelperClient::push (Command c)
{
    {
        const juce::ScopedLock sl (lock);
        commands.add (std::move (c));
    }
    notify();
}

void HelperClient::startLogin (const juce::String& provider)   { push (Command ("login", provider)); }
void HelperClient::cancelLogin()                               { push (Command ("cancel-login")); }
void HelperClient::submitToken (const juce::String& token)     { push (Command ("token", token)); }
void HelperClient::logout (const juce::String& accountId)      { push (Command ("logout", accountId)); }
void HelperClient::uploadExisting()                            { push (Command ("upload-existing")); }
void HelperClient::retry (const juce::String& file)            { push (Command ("retry", file)); }
void HelperClient::setPaused (bool p)                          { push (Command ("pause", {}, {}, p)); }

HelperClient::Reply HelperClient::call (const juce::String& method, const juce::String& path, const juce::var& body)
{
    Reply r;
    juce::URL url (baseUrl + path);
    const bool post = method == "POST";
    if (post)
        url = url.withPOSTData (body.isVoid() ? juce::String ("{}") : juce::JSON::toString (body, true));

    int status = 0;
    auto options = juce::URL::InputStreamOptions (post ? juce::URL::ParameterHandling::inPostData
                                                       : juce::URL::ParameterHandling::inAddress)
                       .withExtraHeaders ("Authorization: Bearer " + secret + "\r\nContent-Type: application/json")
                       .withConnectionTimeoutMs (800)
                       .withNumRedirectsToFollow (0)
                       .withStatusCode (&status)
                       .withHttpRequestCmd (method);

    if (auto stream = url.createInputStream (options))
    {
        r.body = stream->readEntireStreamAsString();
        r.status = status;
        r.json = juce::JSON::parse (r.body);
    }
    return r;
}

void HelperClient::launchHelper()
{
    auto now = juce::Time::currentTimeMillis();
    if (now - lastLaunch < 5000)
        return; // do not hammer a helper that fails to start
    lastLaunch = now;

    auto exe = bundledHelper();
    if (! exe.existsAsFile())
        return;

    // "--detach" makes the helper restart itself outside this process tree
    // and return at once, so nothing here waits for it.
    juce::ChildProcess launcher;
    launcher.start (juce::StringArray { exe.getFullPathName(), "--detach" }, 0);
    launcher.waitForProcessToFinish (2000);
}

bool HelperClient::connect()
{
    auto infoFile = dataDirectory().getChildFile ("helper.json");
    auto readInfo = [&]() -> bool
    {
        auto info = juce::JSON::parse (infoFile);
        auto port = integer (info, "port");
        if (port <= 0)
            return false;
        baseUrl = "http://127.0.0.1:" + juce::String (port);
        secret = str (info, "secret");
        return true;
    };

    if (readInfo())
    {
        auto reply = call ("GET", "/v1/status?client=" + clientId);
        if (reply.ok())
        {
            // A helper older than the one we carry makes way for ours; its
            // queue is on disk and the new one continues it.
            if (olderThan (str (reply.json, "version"), AUDIO_BACKUPER_HELPER_VERSION)
                && bundledHelper().existsAsFile())
            {
                call ("POST", "/v1/shutdown");
                for (int i = 0; i < 30 && infoFile.existsAsFile() && pause (100); ++i) {}
                lastLaunch = 0;
            }
            else
            {
                return true;
            }
        }
    }

    launchHelper();
    for (int i = 0; i < 30 && ! threadShouldExit(); ++i)
    {
        if (readInfo() && call ("GET", "/v1/status?client=" + clientId).ok())
            return true;
        if (! pause (100))
            break;
    }
    return false;
}

Snapshot HelperClient::parseStatus (const juce::var& v)
{
    Snapshot s;
    s.connected = true;
    s.helperVersion = str (v, "version");
    s.paused = flag (v, "paused");
    s.secretsInFile = flag (v, "secretsInFile");

    if (auto* list = v.getProperty ("providers", {}).getArray())
        for (auto& p : *list)
            s.providers.add ({ str (p, "id"), str (p, "name"), flag (p, "configured"), flag (p, "manualToken") });

    if (auto* list = v.getProperty ("accounts", {}).getArray())
        for (auto& a : *list)
            s.accounts.add ({ str (a, "id"), str (a, "provider"), str (a, "name"), str (a, "status"),
                              big (a, "totalBytes"), big (a, "freeBytes") });

    if (auto* list = v.getProperty ("watches", {}).getArray())
        for (auto& w : *list)
        {
            Snapshot::Watch x;
            x.id = str (w, "id");
            x.localPath = str (w, "localPath");
            x.accountId = str (w, "accountId");
            x.remoteFolder = str (w, "remoteFolder");
            x.lastError = str (w, "lastError");
            x.available = flag (w, "available");
            x.pending = integer (w, "pending");
            x.failed = integer (w, "failed");
            x.uploaded = integer (w, "uploaded");
            x.known = integer (w, "known");
            if (auto* fails = w.getProperty ("failures", {}).getArray())
                for (auto& f : *fails)
                    x.failures.add ({ str (f, "file"), str (f, "error") });
            s.watches.add (x);
        }

    if (auto* list = v.getProperty ("uploads", {}).getArray())
        for (auto& u : *list)
            s.uploads.add ({ str (u, "watchId"), str (u, "file"), big (u, "sent"), big (u, "size") });

    if (auto* list = v.getProperty ("recent", {}).getArray())
        for (auto& e : *list)
            s.recent.add ({ big (e, "time"), str (e, "watchId"), str (e, "file"), str (e, "kind"), str (e, "detail") });

    return s;
}

void HelperClient::publish (const Snapshot& s)
{
    {
        const juce::ScopedLock sl (lock);
        snapshot = s;
    }
    triggerAsyncUpdate();
}

void HelperClient::handleAsyncUpdate()
{
    juce::String url;
    {
        const juce::ScopedLock sl (lock);
        url.swapWith (urlToOpen);
    }
    if (url.isNotEmpty())
        juce::URL (url).launchInDefaultBrowser();
    if (onChange)
        onChange();
}

void HelperClient::ensureWatch()
{
    WatchSpec want;
    Snapshot current;
    {
        const juce::ScopedLock sl (lock);
        want = spec;
        current = snapshot;
    }

    // A project opened on a machine where its account is unknown, or a fresh
    // instance: use the account that is already connected here, if any.
    if ((want.accountId.isEmpty() || current.account (want.accountId) == nullptr) && ! current.accounts.isEmpty())
    {
        want.accountId = current.accounts.getFirst().id;
        watchDirty = true;
        // Remember it at once: the interface moves on to choosing a folder
        // as soon as this instance has an account, before any folder exists.
        {
            const juce::ScopedLock sl (lock);
            spec.accountId = want.accountId;
        }
        triggerAsyncUpdate();
    }
    if (want.id.isNotEmpty() && current.watch (want.id) == nullptr)
        watchDirty = true; // the helper does not know this binding (yet, or any more)

    if (! watchDirty || want.localPath.isEmpty() || current.account (want.accountId) == nullptr)
        return;

    auto reply = call ("POST", "/v1/watch", object ({ { "id", want.id }, { "localPath", want.localPath },
                                                      { "accountId", want.accountId }, { "remoteFolder", want.remoteFolder },
                                                      { "recursive", want.recursive }, { "overwrite", want.overwrite } }));
    if (! reply.ok())
        return;

    want.id = str (reply.json, "id");
    watchDirty = false;
    const juce::ScopedLock sl (lock);
    // Keep what the user may have changed meanwhile; only fill in what the helper decided.
    spec.id = want.id;
    if (spec.accountId.isEmpty() || current.account (spec.accountId) == nullptr)
        spec.accountId = want.accountId;
}

void HelperClient::execute (const Command& c)
{
    WatchSpec mine = getWatch();
    juce::String notice;
    auto fail = [&] (const Reply& r, const juce::String& fallback)
    {
        auto e = str (r.json, "error");
        notice = e.isNotEmpty() ? e : fallback;
    };

    if (c.type == "ensure")
    {
        watchDirty = true;
    }
    else if (c.type == "login")
    {
        auto r = call ("POST", "/v1/login", object ({ { "provider", c.a } }));
        Snapshot::Login login;
        if (r.ok())
        {
            login = { str (r.json, "id"), str (r.json, "provider"), str (r.json, "url"),
                      str (r.json, "userCode"), str (r.json, "state"), {} };
        }
        else
        {
            login.state = "failed";
            login.provider = c.a;
            login.error = str (r.json, "error");
            if (login.error.isEmpty())
                login.error = "The helper did not answer";
        }
        const juce::ScopedLock sl (lock);
        snapshot.login = login;
        if (r.ok())
            urlToOpen = login.url;
    }
    else if (c.type == "cancel-login")
    {
        const juce::ScopedLock sl (lock);
        snapshot.login = {};
    }
    else if (c.type == "token")
    {
        auto r = call ("POST", "/v1/login/token", object ({ { "provider", "yandex" }, { "token", c.a } }));
        if (! r.ok())
            fail (r, "The token was not accepted");
        watchDirty = true;
    }
    else if (c.type == "logout")
    {
        call ("POST", "/v1/logout", object ({ { "accountId", c.a } }));
    }
    else if (c.type == "upload-existing" && mine.id.isNotEmpty())
    {
        call ("POST", "/v1/watch/upload-existing", object ({ { "id", mine.id } }));
    }
    else if (c.type == "retry")
    {
        call ("POST", "/v1/retry", object ({ { "watchId", mine.id }, { "file", c.a } }));
    }
    else if (c.type == "pause")
    {
        call ("POST", "/v1/pause", object ({ { "paused", c.flag } }));
    }

    if (notice.isNotEmpty())
    {
        const juce::ScopedLock sl (lock);
        snapshot.notice = notice;
    }
}

void HelperClient::poll()
{
    auto mine = getWatch();
    auto path = "/v1/status?client=" + clientId;
    if (mine.id.isNotEmpty())
        path += "&watch=" + juce::URL::addEscapeChars (mine.id, true);

    auto reply = call ("GET", path);
    if (! reply.ok())
    {
        Snapshot s;
        s.problem = "The helper stopped answering";
        {
            const juce::ScopedLock sl (lock);
            s.login = snapshot.login;
        }
        baseUrl.clear();
        publish (s);
        return;
    }

    auto s = parseStatus (reply.json);
    Snapshot::Login login;
    {
        const juce::ScopedLock sl (lock);
        login = snapshot.login;
        s.notice = snapshot.notice;
    }

    if (login.state == "pending" && login.id.isNotEmpty())
    {
        auto r = call ("GET", "/v1/login/" + login.id);
        if (r.ok())
        {
            login.state = str (r.json, "state");
            login.error = str (r.json, "error");
            if (login.state == "done")
            {
                // The freshly connected account becomes this instance's account.
                auto account = str (r.json, "accountId");
                {
                    const juce::ScopedLock sl (lock);
                    spec.accountId = account;
                }
                watchDirty = true;
                login = {};
            }
        }
        else if (r.status == 404)
        {
            login = {};
        }
    }
    s.login = login;
    publish (s);
}

void HelperClient::run()
{
    while (! threadShouldExit())
    {
        if (baseUrl.isEmpty())
        {
            if (! connect())
            {
                Snapshot s;
                s.problem = bundledHelper().existsAsFile() ? "Starting the helper..."
                                                           : "The helper is missing from the plugin bundle";
                publish (s);
                pause (1000);
                continue;
            }
            watchDirty = true;
        }

        juce::Array<Command> todo;
        {
            const juce::ScopedLock sl (lock);
            todo.swapWith (commands);
        }
        for (auto& c : todo)
            execute (c);

        poll();
        if (baseUrl.isNotEmpty())
        {
            ensureWatch();
            if (! todo.isEmpty())
                poll(); // show the effect of a command without waiting a cycle
        }

        bool busy;
        {
            const juce::ScopedLock sl (lock);
            busy = interfaceOpen || ! snapshot.uploads.isEmpty() || snapshot.login.state == "pending";
        }
        // Each request also tells the helper this instance is alive; it
        // forgets a client after 15 seconds of silence.
        pause (busy ? 700 : 4000);
    }
}

bool HelperClient::pause (int milliseconds)
{
    // The wake-up sent on shutdown is consumed by the first wait it meets;
    // checking first keeps a second wait from sleeping through the exit.
    if (threadShouldExit())
        return false;
    wait (milliseconds);
    return ! threadShouldExit();
}
