#pragma once

#include <juce_audio_processors/juce_audio_processors.h>
#include "PluginProcessor.h"

/**
 * AudioBackuperEditor
 *
 * UI плагина: панель управления бэкапером.
 * Никаких аудио-элементов — только настройки сервиса.
 */
class AudioBackuperEditor : public juce::AudioProcessorEditor,
                            private juce::Timer
{
public:
    explicit AudioBackuperEditor(AudioBackuperProcessor&);
    ~AudioBackuperEditor() override;

    void paint(juce::Graphics&) override;
    void resized() override;

private:
    void timerCallback() override;
    void updateStatusUI();
    void appendLog(const juce::String& message);
    void chooseWatchFolder();
    void openYandexTokenDialog();

    AudioBackuperProcessor& processor;
    BackuperService& service;

    // ── UI элементы ───────────────────────────────────────────────────────────
    juce::Label titleLabel;
    juce::Label statusLabel;
    juce::Label folderLabel;
    juce::Label folderPathLabel;
    juce::Label pendingLabel;
    juce::Label lastSyncLabel;

    juce::TextButton chooseFolderButton  { "Choose Folder" };
    juce::TextButton yandexTokenButton   { "Set Yandex Token" };
    juce::TextButton startStopButton     { "Start" };
    juce::TextButton syncNowButton       { "Sync Now" };

    juce::ToggleButton pauseToggle       { "Pause backups" };

    // Индикатор статуса
    juce::Label statusDot;

    // Лог ошибок (последние события)
    juce::TextEditor logBox;

    // FileChooser должен жить на куче — не уничтожается до завершения диалога
    std::unique_ptr<juce::FileChooser> chooser;

    JUCE_DECLARE_NON_COPYABLE_WITH_LEAK_DETECTOR(AudioBackuperEditor)
};
