#include "BackuperService.h"

BackuperService::BackuperService()
    : http(SERVICE_PORT)
{
    startTimer(2000); // опрашиваем сервис каждые 2 секунды
}

BackuperService::~BackuperService()
{
    stopTimer();
    if (!isUserStopped)
        stop();
}

// ── Запуск ────────────────────────────────────────────────────────────────────

void BackuperService::startIfNotRunning()
{
    if (currentStatus == Status::Running || currentStatus == Status::Starting)
        return;

    auto node = findNodeExecutable();
    auto script = findServiceScript();

    if (!node.existsAsFile())
    {
        lastError = "Node.js not found. Install Node.js from nodejs.org";
        updateStatus(Status::Error);
        return;
    }

    if (!script.existsAsFile())
    {
        lastError = "Service script not found: " + script.getFullPathName();
        updateStatus(Status::Error);
        return;
    }

    nodeProcess = std::make_unique<juce::ChildProcess>();

    juce::StringArray args;
    args.add(node.getFullPathName());
    args.add(script.getFullPathName());
    args.add("--port=" + juce::String(SERVICE_PORT));

    isUserStopped = false;
    startupRetries = 0;
    updateStatus(Status::Starting);

    if (!nodeProcess->start(args))
    {
        lastError = "Failed to start Node.js process";
        updateStatus(Status::Error);
        nodeProcess.reset();
    }
}

void BackuperService::stop()
{
    isUserStopped = true;

    // Сначала пробуем мягко остановить через API
    http.post("/shutdown", {});

    juce::Thread::sleep(500);

    // Если процесс ещё жив — убиваем
    if (nodeProcess && nodeProcess->isRunning())
        nodeProcess->kill();

    nodeProcess.reset();
    updateStatus(Status::Stopped);
}

void BackuperService::restart()
{
    stop();
    juce::Thread::sleep(300);
    isUserStopped = false;
    startIfNotRunning();
}

// ── Команды ───────────────────────────────────────────────────────────────────

void BackuperService::setWatchFolder(const juce::String& path)
{
    watchFolder = path;
    http.post("/config", { { "watchFolder", path } });
}

void BackuperService::setYandexToken(const juce::String& token)
{
    http.post("/config", { { "yandexToken", token } });
}

void BackuperService::pauseBackup()  { http.post("/pause", {}); }
void BackuperService::resumeBackup() { http.post("/resume", {}); }
void BackuperService::syncNow()      { http.post("/sync-now", {}); }

// ── Статус ────────────────────────────────────────────────────────────────────

juce::String BackuperService::getStatusText() const
{
    switch (currentStatus)
    {
        case Status::Stopped:  return "Stopped";
        case Status::Starting: return "Starting...";
        case Status::Running:  return "Running";
        case Status::Error:    return "Error: " + lastError;
    }
    return {};
}

// ── Timer — опрос состояния ───────────────────────────────────────────────────

void BackuperService::timerCallback()
{
    if (isUserStopped)
        return;

    if (currentStatus == Status::Starting || currentStatus == Status::Running)
        pollServiceStatus();

    // Проверяем не упал ли процесс
    if (nodeProcess && !nodeProcess->isRunning() && currentStatus == Status::Running)
    {
        lastError = "Service process died unexpectedly";
        updateStatus(Status::Error);
        nodeProcess.reset();
    }
}

void BackuperService::pollServiceStatus()
{
    auto response = http.get("/status");

    if (response.success)
    {
        auto json = juce::JSON::parse(response.body);
        if (auto* obj = json.getDynamicObject())
        {
            pendingFiles  = (int) obj->getProperty("pendingFiles");
            lastSyncedFile = obj->getProperty("lastSyncedFile").toString();

            if (currentStatus != Status::Running)
                updateStatus(Status::Running);
            else if (onStatusChanged)
                onStatusChanged(); // обновляем счётчики в UI
        }
    }
    else if (currentStatus == Status::Starting)
    {
        startupRetries++;
        if (startupRetries > MAX_STARTUP_RETRIES)
        {
            lastError = "Service did not start in time";
            updateStatus(Status::Error);
        }
    }
}

void BackuperService::updateStatus(Status newStatus)
{
    currentStatus = newStatus;
    if (onStatusChanged)
        onStatusChanged();
}

// ── Вспомогательные ───────────────────────────────────────────────────────────

juce::File BackuperService::findNodeExecutable() const
{
    // macOS: типичные пути Node.js
    juce::StringArray candidates = {
        "/usr/local/bin/node",
        "/opt/homebrew/bin/node",
        "/usr/bin/node",
    };

#if JUCE_WINDOWS
    candidates = { "C:\\Program Files\\nodejs\\node.exe",
                   "C:\\Program Files (x86)\\nodejs\\node.exe" };
#endif

    for (auto& path : candidates)
    {
        juce::File f(path);
        if (f.existsAsFile()) return f;
    }

    return juce::File("/usr/local/bin/node"); // fallback
}

juce::File BackuperService::findServiceScript() const
{
    // Скрипт лежит рядом с плагином:
    // macOS: ~/Library/Audio/Plug-Ins/VST3/AudioBackuper.vst3/Contents/Resources/service/index.js
    auto pluginDir = juce::File::getSpecialLocation(juce::File::currentApplicationFile)
                         .getParentDirectory();

    return pluginDir.getChildFile("Resources/service/index.js");
}
