#include "PluginEditor.h"

// ── Цветовая схема ────────────────────────────────────────────────────────────
namespace Colors
{
    const juce::Colour bg        { 0xFF1A1A2E };
    const juce::Colour surface   { 0xFF16213E };
    const juce::Colour card      { 0xFF0F1B30 };
    const juce::Colour accent    { 0xFF0F3460 };
    const juce::Colour highlight { 0xFFE94560 };
    const juce::Colour text      { 0xFFEEEEEE };
    const juce::Colour subtext   { 0xFF7B8FA1 };
    const juce::Colour green     { 0xFF4CAF50 };
    const juce::Colour yellow    { 0xFFFFB300 };
    const juce::Colour red       { 0xFFE53935 };
}

// ── Кастомный LookAndFeel ─────────────────────────────────────────────────────
class BackuperLookAndFeel : public juce::LookAndFeel_V4
{
public:
    void drawButtonBackground(juce::Graphics& g, juce::Button& button,
                              const juce::Colour& backgroundColour,
                              bool isHighlighted, bool isDown) override
    {
        auto bounds = button.getLocalBounds().toFloat().reduced(0.5f);
        auto col = backgroundColour;
        if (isDown)            col = col.darker(0.2f);
        else if (isHighlighted) col = col.brighter(0.1f);
        g.setColour(col);
        g.fillRoundedRectangle(bounds, 5.0f);
        g.setColour(col.brighter(0.3f));
        g.drawRoundedRectangle(bounds, 5.0f, 1.0f);
    }

    void drawButtonText(juce::Graphics& g, juce::TextButton& button, bool, bool) override
    {
        g.setFont(juce::Font(12.5f, juce::Font::bold));
        g.setColour(button.findColour(juce::TextButton::textColourOffId));
        g.drawText(button.getButtonText(), button.getLocalBounds(),
                   juce::Justification::centred, false);
    }
};

static BackuperLookAndFeel globalLAF;

// ─────────────────────────────────────────────────────────────────────────────

AudioBackuperEditor::AudioBackuperEditor(AudioBackuperProcessor& p)
    : AudioProcessorEditor(&p), processor(p), service(p.getBackuperService())
{
    setSize(440, 500);
    setLookAndFeel(&globalLAF);

    service.onStatusChanged = [this]
    {
        juce::MessageManager::callAsync([this] { updateStatusUI(); });
    };

    // ── Title ─────────────────────────────────────────────────────────────────
    titleLabel.setText("AUDIO BACKUPER", juce::dontSendNotification);
    titleLabel.setFont(juce::Font(20.0f, juce::Font::bold));
    titleLabel.setColour(juce::Label::textColourId, Colors::highlight);
    titleLabel.setJustificationType(juce::Justification::centredLeft);
    addAndMakeVisible(titleLabel);

    // ── Status ────────────────────────────────────────────────────────────────
    statusDot.setText("●", juce::dontSendNotification);
    statusDot.setFont(juce::Font(16.0f));
    statusDot.setJustificationType(juce::Justification::centred);
    addAndMakeVisible(statusDot);

    statusLabel.setFont(juce::Font(13.0f));
    statusLabel.setColour(juce::Label::textColourId, Colors::subtext);
    addAndMakeVisible(statusLabel);

    // ── Folder ────────────────────────────────────────────────────────────────
    folderLabel.setText("WATCH FOLDER", juce::dontSendNotification);
    folderLabel.setFont(juce::Font(10.0f, juce::Font::bold));
    folderLabel.setColour(juce::Label::textColourId, Colors::subtext);
    addAndMakeVisible(folderLabel);

    folderPathLabel.setFont(juce::Font(12.0f));
    folderPathLabel.setColour(juce::Label::textColourId, Colors::text);
    folderPathLabel.setMinimumHorizontalScale(0.5f);
    addAndMakeVisible(folderPathLabel);

    // ── Stats ─────────────────────────────────────────────────────────────────
    pendingLabel.setFont(juce::Font(13.0f, juce::Font::bold));
    pendingLabel.setColour(juce::Label::textColourId, Colors::text);
    pendingLabel.setJustificationType(juce::Justification::centred);
    addAndMakeVisible(pendingLabel);

    lastSyncLabel.setFont(juce::Font(10.5f));
    lastSyncLabel.setColour(juce::Label::textColourId, Colors::subtext);
    lastSyncLabel.setJustificationType(juce::Justification::centred);
    addAndMakeVisible(lastSyncLabel);

    // ── Log box ───────────────────────────────────────────────────────────────
    logBox.setMultiLine(true);
    logBox.setReadOnly(true);
    logBox.setScrollbarsShown(true);
    logBox.setFont(juce::Font(juce::Font::getDefaultMonospacedFontName(), 10.5f, 0));
    logBox.setColour(juce::TextEditor::backgroundColourId, Colors::card);
    logBox.setColour(juce::TextEditor::textColourId,       Colors::subtext);
    logBox.setColour(juce::TextEditor::outlineColourId,    Colors::accent);
    logBox.setText("Waiting for service...\n");
    addAndMakeVisible(logBox);

    // ── Buttons ───────────────────────────────────────────────────────────────
    auto styleBtn = [](juce::TextButton& btn, juce::Colour bg, juce::Colour fg)
    {
        btn.setColour(juce::TextButton::buttonColourId,  bg);
        btn.setColour(juce::TextButton::textColourOffId, fg);
        btn.setColour(juce::TextButton::textColourOnId,  fg);
    };

    styleBtn(chooseFolderButton, Colors::accent, Colors::text);
    chooseFolderButton.onClick = [this] { chooseWatchFolder(); };
    addAndMakeVisible(chooseFolderButton);

    styleBtn(yandexTokenButton, Colors::accent, Colors::text);
    yandexTokenButton.onClick = [this] { openYandexTokenDialog(); };
    addAndMakeVisible(yandexTokenButton);

    styleBtn(startStopButton, Colors::green, Colors::text);
    startStopButton.onClick = [this]
    {
        if (service.getStatus() == BackuperService::Status::Running ||
            service.getStatus() == BackuperService::Status::Starting)
        {
            service.stop();
            appendLog("Service stopped by user.");
        }
        else
        {
            service.startIfNotRunning();
            appendLog("Starting service...");
        }
    };
    addAndMakeVisible(startStopButton);

    styleBtn(syncNowButton, Colors::highlight, Colors::text);
    syncNowButton.onClick = [this] { service.syncNow(); appendLog("Manual sync triggered."); };
    addAndMakeVisible(syncNowButton);

    pauseToggle.setColour(juce::ToggleButton::textColourId, Colors::text);
    pauseToggle.onClick = [this]
    {
        if (pauseToggle.getToggleState()) { service.pauseBackup();  appendLog("Backup paused."); }
        else                             { service.resumeBackup(); appendLog("Backup resumed."); }
    };
    addAndMakeVisible(pauseToggle);

    startTimer(3000);
    updateStatusUI();
}

AudioBackuperEditor::~AudioBackuperEditor()
{
    service.onStatusChanged = nullptr;
    stopTimer();
    setLookAndFeel(nullptr);
}

// ── Paint ─────────────────────────────────────────────────────────────────────

void AudioBackuperEditor::paint(juce::Graphics& g)
{
    g.fillAll(Colors::bg);

    // Линия под заголовком
    g.setColour(Colors::accent);
    g.drawHorizontalLine(50, 20.0f, (float)(getWidth() - 20));

    // Карточка статистики
    juce::Rectangle<float> statsCard { 20.0f, 168.0f, (float)(getWidth() - 40), 74.0f };
    g.setColour(Colors::card);
    g.fillRoundedRectangle(statsCard, 8.0f);
    g.setColour(Colors::accent);
    g.drawRoundedRectangle(statsCard, 8.0f, 1.0f);

    // Вертикальный разделитель внутри карточки
    g.setColour(Colors::accent);
    g.drawVerticalLine((int)statsCard.getCentreX(), statsCard.getY() + 10, statsCard.getBottom() - 10);

    // Подписи EVENT LOG
    g.setFont(juce::Font(9.5f, juce::Font::bold));
    g.setColour(Colors::subtext);
    g.drawText("EVENT LOG", juce::Rectangle<int>(20, 260, 100, 14),
               juce::Justification::centredLeft);
}

// ── Layout ────────────────────────────────────────────────────────────────────

void AudioBackuperEditor::resized()
{
    auto area = getLocalBounds().reduced(20);

    titleLabel.setBounds(area.removeFromTop(30));
    area.removeFromTop(10); // divider

    // Status row
    auto statusRow = area.removeFromTop(22);
    statusDot.setBounds(statusRow.removeFromLeft(22));
    statusRow.removeFromLeft(6);
    statusLabel.setBounds(statusRow);
    area.removeFromTop(14);

    // Folder
    folderLabel.setBounds(area.removeFromTop(14));
    area.removeFromTop(4);
    auto folderRow = area.removeFromTop(24);
    chooseFolderButton.setBounds(folderRow.removeFromRight(115));
    folderRow.removeFromRight(8);
    folderPathLabel.setBounds(folderRow);
    area.removeFromTop(14);

    // Stats card (y ≈ 168)
    auto statsArea = area.removeFromTop(74);
    auto leftHalf  = statsArea.removeFromLeft(statsArea.getWidth() / 2).reduced(8, 8);
    pendingLabel .setBounds(leftHalf.removeFromTop(28));
    lastSyncLabel.setBounds(leftHalf);
    pauseToggle  .setBounds(statsArea.reduced(12, 22));
    area.removeFromTop(14);

    // Log label gap
    area.removeFromTop(16);

    // Buttons
    auto btnRow = area.removeFromTop(30);
    int  half   = (btnRow.getWidth() - 8) / 2;
    yandexTokenButton.setBounds(btnRow.removeFromLeft(half));
    btnRow.removeFromLeft(8);
    syncNowButton.setBounds(btnRow);
    area.removeFromTop(8);

    startStopButton.setBounds(area.removeFromTop(34));
    area.removeFromTop(8);

    // Log fills the rest
    logBox.setBounds(area);
}

// ── Update UI ─────────────────────────────────────────────────────────────────

void AudioBackuperEditor::updateStatusUI()
{
    auto status = service.getStatus();

    juce::Colour dotColor;
    switch (status)
    {
        case BackuperService::Status::Running:  dotColor = Colors::green;  break;
        case BackuperService::Status::Starting: dotColor = Colors::yellow; break;
        case BackuperService::Status::Error:    dotColor = Colors::red;    break;
        default:                               dotColor = Colors::subtext; break;
    }
    statusDot.setColour(juce::Label::textColourId, dotColor);
    statusLabel.setText(service.getStatusText(), juce::dontSendNotification);

    bool running  = (status == BackuperService::Status::Running);
    bool starting = (status == BackuperService::Status::Starting);

    startStopButton.setButtonText(running ? "Stop Service"
                                 : starting ? "Starting..." : "Start Service");
    startStopButton.setColour(juce::TextButton::buttonColourId,
                              running ? Colors::red : Colors::green);
    startStopButton.setEnabled(!starting);

    syncNowButton.setEnabled(running);
    pauseToggle  .setEnabled(running);

    int pending = service.getPendingFilesCount();
    pendingLabel.setText(juce::String(pending) + (pending == 1 ? " pending" : " pending"),
                         juce::dontSendNotification);
    pendingLabel.setColour(juce::Label::textColourId,
                           pending > 0 ? Colors::yellow : Colors::green);

    auto lastFile = service.getLastSyncedFile();
    lastSyncLabel.setText(lastFile.isEmpty() ? "No files yet" : "\u2191 " + lastFile,
                          juce::dontSendNotification);

    auto folder = service.getWatchFolder();
    folderPathLabel.setText(folder.isEmpty() ? "Not selected" : folder,
                            juce::dontSendNotification);
    folderPathLabel.setColour(juce::Label::textColourId,
                              folder.isEmpty() ? Colors::subtext : Colors::text);

    auto err = service.consumeLastError();
    if (err.isNotEmpty()) appendLog("[ERROR] " + err);

    repaint();
}

void AudioBackuperEditor::timerCallback() { updateStatusUI(); }

// ── Log helper ────────────────────────────────────────────────────────────────

void AudioBackuperEditor::appendLog(const juce::String& message)
{
    auto time = juce::Time::getCurrentTime().toString(false, true, false, true);
    logBox.moveCaretToEnd();
    logBox.insertTextAtCaret("[" + time + "] " + message + "\n");

    // Ограничиваем объём лога
    auto lines = juce::StringArray::fromLines(logBox.getText());
    if (lines.size() > 80)
    {
        lines.removeRange(0, lines.size() - 60);
        logBox.setText(lines.joinIntoString("\n") + "\n");
        logBox.moveCaretToEnd();
    }
}

// ── Dialogs ───────────────────────────────────────────────────────────────────

void AudioBackuperEditor::chooseWatchFolder()
{
    chooser = std::make_unique<juce::FileChooser>(
        "Choose folder to watch for audio files",
        juce::File::getSpecialLocation(juce::File::userMusicDirectory));

    chooser->launchAsync(
        juce::FileBrowserComponent::openMode |
        juce::FileBrowserComponent::canSelectDirectories,
        [this](const juce::FileChooser& fc)
        {
            auto result = fc.getResult();
            if (result.isDirectory())
            {
                service.setWatchFolder(result.getFullPathName());
                appendLog("Watch folder: " + result.getFullPathName());
            }
        });
}

void AudioBackuperEditor::openYandexTokenDialog()
{
    auto* dialog = new juce::AlertWindow(
        "Yandex Disk Token",
        "Paste your Yandex Disk OAuth token below.\nGet one at: oauth.yandex.ru",
        juce::MessageBoxIconType::InfoIcon);

    dialog->addTextEditor("token", "", "OAuth Token:");
    dialog->addButton("Save",   1, juce::KeyPress(juce::KeyPress::returnKey));
    dialog->addButton("Cancel", 0, juce::KeyPress(juce::KeyPress::escapeKey));

    dialog->enterModalState(true,
        juce::ModalCallbackFunction::create([this, dialog](int result)
        {
            if (result == 1)
            {
                auto token = dialog->getTextEditorContents("token").trim();
                if (token.isNotEmpty()) { service.setYandexToken(token); appendLog("Yandex token saved."); }
                else                   { appendLog("[WARN] Empty token — not saved."); }
            }
            delete dialog;
        }), true);
}
