#include "PluginEditor.h"

namespace Colors
{
    const juce::Colour bg        { 0xFF1A1A2E };
    const juce::Colour card      { 0xFF0F1B30 };
    const juce::Colour accent    { 0xFF0F3460 };
    const juce::Colour highlight { 0xFFE94560 };
    const juce::Colour text      { 0xFFEEEEEE };
    const juce::Colour subtext   { 0xFF8FA3B5 };
    const juce::Colour green     { 0xFF4CAF50 };
    const juce::Colour yellow    { 0xFFFFB300 };
    const juce::Colour red       { 0xFFE5534B };
}

namespace
{
    juce::String bytes (juce::int64 n)
    {
        if (n < 0)
            return RU ("без ограничения");
        const double gb = (double) n / (1024.0 * 1024.0 * 1024.0);
        if (gb >= 1.0)
            return juce::String (gb, 1).replaceCharacter ('.', ',') + RU (" ГБ");
        return juce::String ((double) n / (1024.0 * 1024.0), 1).replaceCharacter ('.', ',') + RU (" МБ");
    }

    juce::String cloudName (const Snapshot& s, const juce::String& providerId)
    {
        for (auto& p : s.providers)
            if (p.id == providerId)
                return p.name;
        return providerId;
    }

    juce::String files (int n)
    {
        const int d = n % 10, h = n % 100;
        if (d == 1 && h != 11)                          return juce::String (n) + RU (" файл");
        if (d >= 2 && d <= 4 && (h < 12 || h > 14))     return juce::String (n) + RU (" файла");
        return juce::String (n) + RU (" файлов");
    }

    /** Turns the helper's technical reasons into something to act on. */
    juce::String explain (const juce::String& error)
    {
        if (error.containsIgnoreCase ("network") || error.contains ("HTTP 5") || error.contains ("HTTP 429"))
            return RU ("Нет связи с облаком. Повторяю автоматически.");
        if (error.containsIgnoreCase ("file name"))
            return RU ("Облако не принимает имя файла. Переименуйте файл.");
        if (error.containsIgnoreCase ("permission"))
            return RU ("Нет доступа к файлу на диске.");
        return error;
    }

    void style (juce::TextButton& b, juce::Colour colour)
    {
        b.setColour (juce::TextButton::buttonColourId, colour);
        b.setColour (juce::TextButton::textColourOffId, Colors::text);
        b.setColour (juce::TextButton::textColourOnId, Colors::text);
    }

    void style (juce::Label& l, float size, juce::Colour colour, bool bold = false)
    {
        l.setFont (juce::FontOptions (size, bold ? juce::Font::bold : juce::Font::plain));
        l.setColour (juce::Label::textColourId, colour);
        l.setMinimumHorizontalScale (0.7f);
    }
}

AudioBackuperEditor::AudioBackuperEditor (AudioBackuperProcessor& p)
    : AudioProcessorEditor (&p), client (p.getClient())
{
    setSize (460, 520);

    for (auto* c : std::initializer_list<juce::Component*> {
             &title, &statusLine, &headline, &detail, &accountLine, &folderLine, &errorLine, &hint, &codeLabel,
             &localCaption, &remoteCaption, &remoteFolder, &recent, &progressBar,
             &yandexButton, &googleButton, &tokenButton, &openPageButton, &cancelLoginButton,
             &chooseButton, &doneButton, &signOutButton, &errorButton, &existingButton, &retryButton,
             &settingsButton, &recursiveToggle, &versionsToggle, &pauseToggle })
        addChildComponent (c);

    title.setText ("AUDIO BACKUPER", juce::dontSendNotification);
    style (title, 19.0f, Colors::highlight, true);
    title.setVisible (true);
    style (statusLine, 12.5f, Colors::subtext);
    statusLine.setJustificationType (juce::Justification::centredRight);
    statusLine.setVisible (true);

    style (headline, 17.0f, Colors::text, true);
    style (detail, 13.0f, Colors::subtext);
    detail.setJustificationType (juce::Justification::topLeft);
    style (accountLine, 12.5f, Colors::subtext);
    style (folderLine, 12.5f, Colors::subtext);
    style (errorLine, 13.0f, Colors::yellow);
    errorLine.setJustificationType (juce::Justification::topLeft);
    style (hint, 12.0f, Colors::subtext);
    hint.setJustificationType (juce::Justification::topLeft);
    style (codeLabel, 30.0f, Colors::text, true);
    codeLabel.setJustificationType (juce::Justification::centred);
    style (localCaption, 10.5f, Colors::subtext, true);
    style (remoteCaption, 10.5f, Colors::subtext, true);
    localCaption.setText (RU ("ПАПКА С РЕНДЕРАМИ"), juce::dontSendNotification);
    remoteCaption.setText (RU ("ПАПКА В ОБЛАКЕ"), juce::dontSendNotification);

    for (auto* e : { &remoteFolder, &recent })
    {
        e->setColour (juce::TextEditor::backgroundColourId, Colors::card);
        e->setColour (juce::TextEditor::textColourId, Colors::text);
        e->setColour (juce::TextEditor::outlineColourId, Colors::accent);
        e->setColour (juce::TextEditor::focusedOutlineColourId, Colors::highlight);
    }
    recent.setMultiLine (true);
    recent.setReadOnly (true);
    recent.setCaretVisible (false);
    recent.setFont (juce::FontOptions (12.0f));
    recent.setColour (juce::TextEditor::textColourId, Colors::subtext);
    progressBar.setColour (juce::ProgressBar::foregroundColourId, Colors::green);
    progressBar.setColour (juce::ProgressBar::backgroundColourId, Colors::card);
    progressBar.setPercentageDisplay (true);

    style (yandexButton, Colors::accent);
    style (googleButton, Colors::accent);
    style (tokenButton, Colors::card);
    style (openPageButton, Colors::accent);
    style (cancelLoginButton, Colors::card);
    style (chooseButton, Colors::accent);
    style (doneButton, Colors::green.darker (0.3f));
    style (signOutButton, Colors::card);
    style (errorButton, Colors::highlight.darker (0.2f));
    style (existingButton, Colors::accent);
    style (retryButton, Colors::accent);
    style (settingsButton, Colors::card);

    tokenButton.setButtonText (RU ("Вставить токен Яндекса (для разработки)"));
    openPageButton.setButtonText (RU ("Открыть страницу ещё раз"));
    cancelLoginButton.setButtonText (RU ("Отмена"));
    chooseButton.setButtonText (RU ("Выбрать папку…"));
    doneButton.setButtonText (RU ("Начать копирование"));
    signOutButton.setButtonText (RU ("Выйти из облака"));
    settingsButton.setButtonText (RU ("Настройки"));
    recursiveToggle.setButtonText (RU ("Включая вложенные папки"));
    versionsToggle.setButtonText (RU ("Хранить прежние версии изменённых файлов"));
    pauseToggle.setButtonText (RU ("Пауза"));
    for (auto* t : { &recursiveToggle, &versionsToggle, &pauseToggle })
        t->setColour (juce::ToggleButton::textColourId, Colors::text);

    yandexButton.onClick = [this] { client.startLogin ("yandex"); };
    googleButton.onClick = [this] { client.startLogin ("gdrive"); };
    tokenButton.onClick = [this] { askForToken(); };
    cancelLoginButton.onClick = [this] { client.cancelLogin(); };
    openPageButton.onClick = [this]
    {
        auto url = client.getSnapshot().login.url;
        if (url.isNotEmpty())
            juce::URL (url).launchInDefaultBrowser();
    };
    chooseButton.onClick = [this] { chooseFolder(); };
    doneButton.onClick = [this] { applyFolderSettings(); };
    signOutButton.onClick = [this]
    {
        auto w = client.getWatch();
        client.logout (w.accountId);
        editingFolder = false;
    };
    settingsButton.onClick = [this]
    {
        draft = client.getWatch();
        editingFolder = true;
        refresh();
    };
    pauseToggle.onClick = [this] { client.setPaused (pauseToggle.getToggleState()); };
    existingButton.onClick = [this] { client.uploadExisting(); };
    retryButton.onClick = [this] { client.retry ({}); };
    errorButton.onClick = [this]
    {
        if (errorAction == "login")        client.startLogin (errorProvider);
        else if (errorAction == "retry")   client.retry ({});
        else if (errorAction == "folder")  { draft = client.getWatch(); editingFolder = true; refresh(); }
    };

    draft = client.getWatch();
    client.onChange = [this] { refresh(); };
    client.setInterfaceOpen (true);
    refresh();
}

AudioBackuperEditor::~AudioBackuperEditor()
{
    client.onChange = nullptr;
    client.setInterfaceOpen (false);
}

void AudioBackuperEditor::paint (juce::Graphics& g)
{
    g.fillAll (Colors::bg);
    g.setColour (Colors::accent);
    g.drawHorizontalLine (52, 20.0f, (float) getWidth() - 20.0f);
}

AudioBackuperEditor::Page AudioBackuperEditor::choosePage (const Snapshot& s, const WatchSpec& w) const
{
    if (! s.connected)                          return Page::connecting;
    if (s.login.state == "pending")             return Page::loginWait;
    if (s.account (w.accountId) == nullptr)     return Page::signIn;
    if (w.localPath.isEmpty() || editingFolder) return Page::folder;
    return Page::working;
}

void AudioBackuperEditor::showPage (Page p)
{
    page = p;
    auto show = [] (std::initializer_list<juce::Component*> list, bool visible)
    {
        for (auto* c : list)
            c->setVisible (visible);
    };
    show ({ &headline, &detail, &accountLine, &folderLine, &errorLine, &hint, &codeLabel, &localCaption, &remoteCaption,
            &remoteFolder, &recent, &progressBar, &yandexButton, &googleButton, &tokenButton, &openPageButton,
            &cancelLoginButton, &chooseButton, &doneButton, &signOutButton, &errorButton, &existingButton,
            &retryButton, &settingsButton, &recursiveToggle, &versionsToggle, &pauseToggle }, false);

    switch (p)
    {
        case Page::connecting: show ({ &headline, &detail }, true); break;
        case Page::signIn:     show ({ &headline, &detail, &yandexButton, &googleButton, &tokenButton, &errorLine }, true); break;
        case Page::loginWait:  show ({ &headline, &detail, &codeLabel, &openPageButton, &cancelLoginButton }, true); break;
        case Page::folder:     show ({ &headline, &accountLine, &localCaption, &folderLine, &chooseButton, &remoteCaption,
                                       &remoteFolder, &recursiveToggle, &versionsToggle, &hint, &doneButton, &signOutButton }, true); break;
        case Page::working:    show ({ &headline, &detail, &accountLine, &folderLine, &recent, &pauseToggle, &settingsButton }, true); break;
    }
    resized();
}

void AudioBackuperEditor::refresh()
{
    auto s = client.getSnapshot();
    auto w = client.getWatch();
    auto next = choosePage (s, w);
    if (next != page || ! headline.isVisible())
    {
        if (next == Page::folder && page != Page::folder)
        {
            draft = w;
            remoteFolder.setText (draft.remoteFolder, juce::dontSendNotification);
        }
        showPage (next);
    }

    statusLine.setText (s.connected ? RU ("помощник ") + s.helperVersion : RU ("нет связи с помощником"),
                        juce::dontSendNotification);
    statusLine.setColour (juce::Label::textColourId, s.connected ? Colors::subtext : Colors::yellow);

    auto* account = s.account (w.accountId);
    juce::String accountText;
    if (account != nullptr)
        accountText = cloudName (s, account->provider) + ": " + account->name
                      + RU (" · свободно ") + bytes (account->free);

    switch (page)
    {
        case Page::connecting:
            headline.setText (RU ("Запускаю помощника…"), juce::dontSendNotification);
            detail.setText (s.problem.contains ("missing")
                                ? RU ("В комплекте плагина нет помощника. Переустановите плагин.")
                                : RU ("Помощник копирует файлы в фоне и не мешает работе в DAW."),
                            juce::dontSendNotification);
            break;

        case Page::signIn:
        {
            headline.setText (RU ("Подключите облако"), juce::dontSendNotification);
            detail.setText (RU ("Вход выполняется на странице облака в браузере. Плагин получает доступ только к своей папке."),
                            juce::dontSendNotification);
            bool yandex = false, google = false, manual = false;
            for (auto& p : s.providers)
            {
                if (p.id == "yandex") { yandex = p.configured; manual = p.manualToken; }
                if (p.id == "gdrive") google = p.configured;
            }
            yandexButton.setEnabled (yandex);
            googleButton.setEnabled (google);
            yandexButton.setButtonText (yandex ? RU ("Войти в Яндекс Диск") : RU ("Яндекс Диск — недоступен в этой сборке"));
            googleButton.setButtonText (google ? RU ("Войти в Google Drive") : RU ("Google Drive — недоступен в этой сборке"));
            tokenButton.setVisible (manual);
            auto problem = s.login.state == "failed" ? s.login.error : s.notice;
            errorLine.setText (problem.isNotEmpty() ? RU ("Не удалось войти: ") + problem : juce::String(),
                               juce::dontSendNotification);
            break;
        }

        case Page::loginWait:
            headline.setText (RU ("Подтвердите вход в браузере"), juce::dontSendNotification);
            detail.setText (s.login.userCode.isNotEmpty()
                                ? RU ("На открывшейся странице ") + cloudName (s, s.login.provider) + RU (" введите код:")
                                : RU ("На открывшейся странице ") + cloudName (s, s.login.provider)
                                      + RU (" разрешите доступ. Окно обновится само."),
                            juce::dontSendNotification);
            codeLabel.setText (s.login.userCode, juce::dontSendNotification);
            break;

        case Page::folder:
            headline.setText (RU ("Что копировать"), juce::dontSendNotification);
            accountLine.setText (accountText, juce::dontSendNotification);
            folderLine.setText (draft.localPath.isEmpty() ? RU ("не выбрана") : draft.localPath, juce::dontSendNotification);
            folderLine.setColour (juce::Label::textColourId, draft.localPath.isEmpty() ? Colors::yellow : Colors::text);
            recursiveToggle.setToggleState (draft.recursive, juce::dontSendNotification);
            versionsToggle.setToggleState (! draft.overwrite, juce::dontSendNotification);
            hint.setText (RU ("Копируются аудиофайлы, которые появятся или изменятся в папке после настройки. "
                              "Уже лежащие в ней файлы можно загрузить отдельной кнопкой."),
                          juce::dontSendNotification);
            doneButton.setEnabled (draft.localPath.isNotEmpty());
            doneButton.setButtonText (w.localPath.isEmpty() ? RU ("Начать копирование") : RU ("Сохранить"));
            break;

        case Page::working:
            accountLine.setText (accountText, juce::dontSendNotification);
            fillWorking (s, w);
            break;
    }
}

void AudioBackuperEditor::fillWorking (const Snapshot& s, const WatchSpec& w)
{
    auto* watch = s.watch (w.id);
    auto* account = s.account (w.accountId);
    folderLine.setText (w.localPath + juce::String::fromUTF8 ("  \xe2\x86\x92  ")
                            + (watch != nullptr ? watch->remoteFolder : w.remoteFolder),
                        juce::dontSendNotification);
    folderLine.setColour (juce::Label::textColourId, Colors::subtext);
    pauseToggle.setToggleState (s.paused, juce::dontSendNotification);

    const Snapshot::Upload* current = nullptr;
    for (auto& u : s.uploads)
        if (u.watchId == w.id)
            current = &u;

    // One problem at a time, the one that blocks the most.
    juce::String problem, button;
    errorAction.clear();
    if (account != nullptr && account->status == "needs_login")
    {
        problem = RU ("Облако просит войти заново. Файлы ждут в очереди и не потеряются.");
        button = RU ("Войти заново");
        errorAction = "login";
        errorProvider = account->provider;
    }
    else if (account != nullptr && account->status == "no_space")
    {
        problem = RU ("В облаке закончилось место. Освободите место и нажмите «Повторить».");
        button = RU ("Повторить");
        errorAction = "retry";
    }
    else if (watch != nullptr && ! watch->available)
    {
        problem = RU ("Папка недоступна: она удалена или диск отключён.");
        button = RU ("Выбрать другую папку");
        errorAction = "folder";
    }
    else if (watch != nullptr && watch->failed > 0)
    {
        auto& f = watch->failures.getReference (0);
        problem = RU ("Не удалось загрузить ") + files (watch->failed) + ": " + f.file + juce::String::fromUTF8 (" \xe2\x80\x94 ") + explain (f.error);
        button = RU ("Повторить");
        errorAction = "retry";
    }
    else if (watch != nullptr && watch->pending > 0 && watch->lastError.isNotEmpty() && current == nullptr)
    {
        problem = explain (watch->lastError);
    }
    else if (s.secretsInFile)
    {
        problem = RU ("В системе нет хранилища секретов: доступ к облаку хранится в файле, закрытом от других пользователей.");
    }

    errorLine.setText (problem, juce::dontSendNotification);
    errorLine.setVisible (problem.isNotEmpty());
    errorButton.setButtonText (button);
    errorButton.setVisible (button.isNotEmpty());

    if (watch == nullptr)
    {
        headline.setText (RU ("Подключаю папку…"), juce::dontSendNotification);
        detail.setText ({}, juce::dontSendNotification);
        progressBar.setVisible (false);
    }
    else if (current != nullptr)
    {
        headline.setText (RU ("Загружается"), juce::dontSendNotification);
        progress = current->size > 0 ? (double) current->sent / (double) current->size : 0.0;
        juce::String more = watch->pending > 1 ? RU (" · в очереди ещё ") + juce::String (watch->pending - 1) : juce::String();
        detail.setText (current->file + more, juce::dontSendNotification);
        progressBar.setVisible (true);
    }
    else if (s.paused && watch->pending > 0)
    {
        headline.setText (RU ("Пауза"), juce::dontSendNotification);
        detail.setText (RU ("В очереди ") + files (watch->pending), juce::dontSendNotification);
        progressBar.setVisible (false);
    }
    else if (watch->pending > 0)
    {
        headline.setText (RU ("В очереди ") + files (watch->pending), juce::dontSendNotification);
        detail.setText ({}, juce::dontSendNotification);
        progressBar.setVisible (false);
    }
    else
    {
        headline.setText (RU ("Всё скопировано"), juce::dontSendNotification);
        detail.setText (RU ("В облаке ") + files (watch->uploaded) + RU (" из этой папки"), juce::dontSendNotification);
        progressBar.setVisible (false);
    }

    const int known = watch != nullptr ? watch->known : 0;
    existingButton.setVisible (known > 0);
    existingButton.setButtonText (RU ("Загрузить уже имеющиеся: ") + files (known));
    retryButton.setVisible (false);

    juce::String log;
    for (auto& e : s.recent)
    {
        if (e.watchId != w.id)
            continue;
        auto time = juce::Time (e.time * 1000).formatted ("%d.%m %H:%M");
        juce::String what = e.kind == "uploaded" ? RU ("загружен")
                          : e.kind == "version"  ? RU ("прежняя версия сохранена")
                          : e.kind == "failed"   ? RU ("не загружен")
                                                 : RU ("пропущен");
        log << time << "  " << e.file << juce::String::fromUTF8 (" \xe2\x80\x94 ") << what << "\n";
    }
    if (log.isEmpty())
        log = RU ("Здесь появятся скопированные файлы.");
    if (recent.getText() != log)
        recent.setText (log, juce::dontSendNotification);

    resized();
}

void AudioBackuperEditor::resized()
{
    auto area = getLocalBounds().reduced (20);
    auto top = area.removeFromTop (30);
    title.setBounds (top.removeFromLeft (220));
    statusLine.setBounds (top);
    area.removeFromTop (16);

    headline.setBounds (area.removeFromTop (26));
    area.removeFromTop (4);

    switch (page)
    {
        case Page::connecting:
            detail.setBounds (area.removeFromTop (60));
            break;

        case Page::signIn:
            detail.setBounds (area.removeFromTop (44));
            area.removeFromTop (12);
            yandexButton.setBounds (area.removeFromTop (38));
            area.removeFromTop (8);
            googleButton.setBounds (area.removeFromTop (38));
            area.removeFromTop (16);
            errorLine.setBounds (area.removeFromTop (48));
            tokenButton.setBounds (area.removeFromBottom (28));
            break;

        case Page::loginWait:
            detail.setBounds (area.removeFromTop (44));
            area.removeFromTop (10);
            codeLabel.setBounds (area.removeFromTop (54));
            area.removeFromTop (16);
            openPageButton.setBounds (area.removeFromTop (34));
            area.removeFromTop (8);
            cancelLoginButton.setBounds (area.removeFromTop (30));
            break;

        case Page::folder:
        {
            accountLine.setBounds (area.removeFromTop (20));
            area.removeFromTop (14);
            localCaption.setBounds (area.removeFromTop (14));
            auto row = area.removeFromTop (30);
            chooseButton.setBounds (row.removeFromRight (140));
            row.removeFromRight (8);
            folderLine.setBounds (row);
            area.removeFromTop (12);
            remoteCaption.setBounds (area.removeFromTop (14));
            area.removeFromTop (2);
            remoteFolder.setBounds (area.removeFromTop (28));
            area.removeFromTop (12);
            recursiveToggle.setBounds (area.removeFromTop (24));
            versionsToggle.setBounds (area.removeFromTop (24));
            area.removeFromTop (8);
            hint.setBounds (area.removeFromTop (54));
            signOutButton.setBounds (area.removeFromBottom (28));
            area.removeFromBottom (8);
            doneButton.setBounds (area.removeFromBottom (38));
            break;
        }

        case Page::working:
        {
            detail.setBounds (area.removeFromTop (20));
            if (progressBar.isVisible())
            {
                area.removeFromTop (6);
                progressBar.setBounds (area.removeFromTop (18));
            }
            area.removeFromTop (10);
            accountLine.setBounds (area.removeFromTop (18));
            folderLine.setBounds (area.removeFromTop (18));
            area.removeFromTop (8);
            if (errorLine.isVisible())
            {
                errorLine.setBounds (area.removeFromTop (52));
                if (errorButton.isVisible())
                {
                    errorButton.setBounds (area.removeFromTop (30).removeFromLeft (220));
                    area.removeFromTop (8);
                }
            }
            auto bottom = area.removeFromBottom (30);
            pauseToggle.setBounds (bottom.removeFromLeft (100));
            settingsButton.setBounds (bottom.removeFromRight (110));
            area.removeFromBottom (8);
            if (existingButton.isVisible())
            {
                existingButton.setBounds (area.removeFromBottom (30));
                area.removeFromBottom (8);
            }
            recent.setBounds (area);
            break;
        }
    }
}

void AudioBackuperEditor::chooseFolder()
{
    juce::File start (draft.localPath);
    if (! start.isDirectory())
    {
        // Logic bounces here by default; elsewhere the Music folder is a fair guess.
        auto music = juce::File::getSpecialLocation (juce::File::userMusicDirectory);
        auto logic = music.getChildFile ("Logic/Bounces");
        start = juce::PluginHostType().isLogic() && logic.isDirectory() ? logic : music;
    }
    chooser = std::make_unique<juce::FileChooser> (RU ("Папка, в которую сохраняются рендеры"), start);
    chooser->launchAsync (juce::FileBrowserComponent::openMode | juce::FileBrowserComponent::canSelectDirectories,
                          [this] (const juce::FileChooser& fc)
                          {
                              auto dir = fc.getResult();
                              if (! dir.isDirectory())
                                  return;
                              const bool followName = remoteFolder.getText().trim().isEmpty()
                                                      || remoteFolder.getText() == juce::File (draft.localPath).getFileName();
                              draft.localPath = dir.getFullPathName();
                              if (followName)
                                  remoteFolder.setText (dir.getFileName(), juce::dontSendNotification);
                              refresh();
                          });
}

void AudioBackuperEditor::applyFolderSettings()
{
    auto w = client.getWatch();
    w.localPath = draft.localPath;
    w.remoteFolder = remoteFolder.getText().trim();
    w.recursive = recursiveToggle.getToggleState();
    w.overwrite = ! versionsToggle.getToggleState();
    editingFolder = false;
    client.setWatch (w);
    refresh();
}

void AudioBackuperEditor::askForToken()
{
    auto* dialog = new juce::AlertWindow (RU ("Токен Яндекс Диска"),
                                          RU ("Вставьте OAuth-токен с доступом к папке приложения. "
                                              "Только для разработки: обычный вход идёт через браузер."),
                                          juce::MessageBoxIconType::NoIcon);
    dialog->addTextEditor ("token", {}, RU ("Токен:"), true);
    dialog->addButton (RU ("Сохранить"), 1, juce::KeyPress (juce::KeyPress::returnKey));
    dialog->addButton (RU ("Отмена"), 0, juce::KeyPress (juce::KeyPress::escapeKey));
    juce::Component::SafePointer<AudioBackuperEditor> self (this);
    dialog->enterModalState (true, juce::ModalCallbackFunction::create ([self, dialog] (int result)
    {
        auto token = dialog->getTextEditorContents ("token").trim();
        if (result == 1 && token.isNotEmpty() && self != nullptr)
            self->client.submitToken (token);
    }), true);
}
