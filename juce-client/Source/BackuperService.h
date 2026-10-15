#pragma once

#include <juce_core/juce_core.h>
#include <juce_events/juce_events.h>
#include "HttpClient.h"

/**
 * BackuperService
 *
 * Управляет жизненным циклом Node.js бэкапера:
 *   - запуск/остановка дочернего процесса
 *   - опрос статуса через HTTP
 *   - передача команд (setWatchFolder, pause, resume, syncNow)
 */
class BackuperService : public juce::Timer
{
public:
    enum class Status { Stopped, Starting, Running, Error };

    BackuperService();
    ~BackuperService() override;

    // ── Управление процессом ──────────────────────────────────────────────────
    void startIfNotRunning();
    void stop();
    void restart();

    // ── Команды к сервису ─────────────────────────────────────────────────────
    void setWatchFolder(const juce::String& path);
    void setYandexToken(const juce::String& token);
    void pauseBackup();
    void resumeBackup();
    void syncNow();

    // ── Состояние ─────────────────────────────────────────────────────────────
    Status getStatus() const { return currentStatus; }
    juce::String getStatusText() const;
    juce::String getWatchFolder() const { return watchFolder; }
    int getPendingFilesCount() const { return pendingFiles; }
    juce::String getLastSyncedFile() const { return lastSyncedFile; }
    juce::String getLastError() const { return lastError; }

    // Возвращает ошибку и сбрасывает её — чтобы UI не показывал одно и то же дважды
    juce::String consumeLastError()
    {
        auto e = lastError;
        lastError.clear();
        return e;
    }

    // ── Callback при изменении статуса ────────────────────────────────────────
    std::function<void()> onStatusChanged;

private:
    // ── juce::Timer ──────────────────────────────────────────────────────────
    void timerCallback() override;

    // ── Внутренние методы ────────────────────────────────────────────────────
    juce::File findNodeExecutable() const;
    juce::File findServiceScript() const;
    void pollServiceStatus();
    void updateStatus(Status newStatus);

    // ── Дочерний процесс ─────────────────────────────────────────────────────
    std::unique_ptr<juce::ChildProcess> nodeProcess;

    // ── HTTP клиент для общения с сервисом ───────────────────────────────────
    HttpClient http;
    static constexpr int SERVICE_PORT = 47291; // уникальный порт

    // ── Состояние ─────────────────────────────────────────────────────────────
    Status currentStatus { Status::Stopped };
    juce::String watchFolder;
    juce::String lastSyncedFile;
    juce::String lastError;
    int pendingFiles { 0 };

    // ── Флаги ────────────────────────────────────────────────────────────────
    bool isUserStopped { false };
    int startupRetries { 0 };
    static constexpr int MAX_STARTUP_RETRIES = 10;

    JUCE_DECLARE_NON_COPYABLE_WITH_LEAK_DETECTOR(BackuperService)
};
