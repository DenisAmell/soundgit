#pragma once

#include <juce_audio_processors/juce_audio_processors.h>
#include "PluginProcessor.h"

/**
    The plugin window: connect a cloud, choose the folder, watch the backup.
    It only shows the latest snapshot from HelperClient and sends commands
    to it; it never waits for anything.
*/
class AudioBackuperEditor : public juce::AudioProcessorEditor
{
public:
    explicit AudioBackuperEditor (AudioBackuperProcessor&);
    ~AudioBackuperEditor() override;

    void paint (juce::Graphics&) override;
    void resized() override;

private:
    enum class Page { connecting, signIn, loginWait, folder, working };

    void refresh();
    Page choosePage (const Snapshot&, const WatchSpec&) const;
    void showPage (Page);
    void fillWorking (const Snapshot&, const WatchSpec&);
    void chooseFolder();
    void applyFolderSettings();
    void askForToken();

    HelperClient& client;
    Page page = Page::connecting;
    bool editingFolder = false;
    WatchSpec draft;                 // settings being edited on the folder page
    juce::String errorAction;        // what the error button does: "login", "retry", "folder"
    juce::String errorProvider;
    double progress = 0.0;

    juce::Label title, statusLine, headline, detail, accountLine, folderLine, errorLine, hint, codeLabel;
    juce::Label localCaption, remoteCaption;
    juce::TextEditor remoteFolder, recent;
    juce::ProgressBar progressBar { progress };
    juce::TextButton yandexButton, googleButton, tokenButton, openPageButton, cancelLoginButton;
    juce::TextButton chooseButton, doneButton, signOutButton;
    juce::TextButton errorButton, existingButton, retryButton, settingsButton;
    juce::ToggleButton recursiveToggle, versionsToggle, pauseToggle;

    std::unique_ptr<juce::FileChooser> chooser;

    JUCE_DECLARE_NON_COPYABLE_WITH_LEAK_DETECTOR (AudioBackuperEditor)
};
