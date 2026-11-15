// Checks of the plugin that need no DAW: the sound passes untouched in every
// channel layout, the project state round-trips, and removing the plugin
// does not wait for anything.

#include "PluginProcessor.h"
#include <cstdlib>
#include <cstring>
#include <iostream>

namespace
{
    int failures = 0;

    void expect (bool condition, const juce::String& what)
    {
        if (! condition)
        {
            ++failures;
            std::cout << "FAIL: " << what << std::endl;
        }
    }

    void setEnv (const char* name, const juce::String& value)
    {
       #if JUCE_WINDOWS
        _putenv_s (name, value.toRawUTF8());
       #else
        setenv (name, value.toRawUTF8(), 1);
       #endif
    }

    template <typename Sample>
    void passThrough (const juce::AudioChannelSet& set, const juce::String& name)
    {
        AudioBackuperProcessor p;
        juce::AudioProcessor::BusesLayout layout;
        layout.inputBuses.add (set);
        layout.outputBuses.add (set);
        expect (p.checkBusesLayoutSupported (layout), name + ": layout is supported");
        expect (p.setBusesLayout (layout), name + ": layout can be set");
        p.setProcessingPrecision (sizeof (Sample) == 8 ? juce::AudioProcessor::doublePrecision
                                                       : juce::AudioProcessor::singlePrecision);
        p.setRateAndBufferSizeDetails (48000.0, 512);

        juce::Random random (42);
        juce::AudioBuffer<Sample> buffer (set.size(), 512), original;
        juce::MidiBuffer midi;
        for (int block = 0; block < 8; ++block)
        {
            for (int c = 0; c < buffer.getNumChannels(); ++c)
                for (int i = 0; i < buffer.getNumSamples(); ++i)
                    buffer.setSample (c, i, (Sample) (random.nextDouble() * 2.0 - 1.0));
            original.makeCopyOf (buffer);
            p.processBlock (buffer, midi);
            bool same = true;
            for (int c = 0; c < buffer.getNumChannels() && same; ++c)
                same = std::memcmp (buffer.getReadPointer (c), original.getReadPointer (c),
                                    sizeof (Sample) * (size_t) buffer.getNumSamples()) == 0;
            expect (same, name + ": audio is bit-identical after processing");
        }
        expect (p.getLatencySamples() == 0, name + ": no latency");
        expect (p.getTailLengthSeconds() == 0.0, name + ": no tail");
    }
}

/** Renders the plugin window to PNG files in each of its states, against a
    running helper (see tools/reaper/backuper-screens.sh). Lets the interface
    be reviewed without opening a DAW. */
static int screenshots (const juce::File& dir, const juce::String& token, const juce::String& folder)
{
    auto pump = [] (int ms) { juce::MessageManager::getInstance()->runDispatchLoopUntil (ms); };
    AudioBackuperProcessor p;
    std::unique_ptr<juce::AudioProcessorEditor> editor (p.createEditor());
    auto shot = [&] (const juce::String& name)
    {
        pump (1200);
        auto image = editor->createComponentSnapshot (editor->getLocalBounds(), true, 2.0f);
        juce::FileOutputStream out (dir.getChildFile (name + ".png"));
        out.setPosition (0);
        out.truncate();
        juce::PNGImageFormat().writeImageToStream (image, out);
        std::cout << "wrote " << name << ".png" << std::endl;
    };
    auto& client = p.getClient();
    for (int i = 0; i < 50 && ! client.getSnapshot().connected; ++i)
        pump (100);
    if (! client.getSnapshot().connected)
    {
        shot ("0-no-helper");
        return 1;
    }
    shot ("1-sign-in");
    client.submitToken (token);
    shot ("2-choose-folder");
    auto w = client.getWatch();
    w.localPath = folder;
    w.remoteFolder = juce::File (folder).getFileName();
    client.setWatch (w);
    shot ("3-working-empty");
    juce::File (folder).getChildFile ("Bounce final mix.wav").replaceWithText (juce::String::repeatedString ("audio", 4000));
    juce::File (folder).getChildFile ("Stems").createDirectory();
    juce::File (folder).getChildFile ("Stems/Drums #2.wav").replaceWithText ("stem");
    pump (4500);
    shot ("4-working-uploaded");
    client.setPaused (true);
    juce::File (folder).getChildFile ("Late render.wav").replaceWithText ("late");
    pump (3500);
    shot ("5-paused-queue");
    client.setPaused (false);
    client.logout (client.getWatch().accountId);
    shot ("6-needs-login");
    return 0;
}

int main (int argc, char* argv[])
{
    juce::ScopedJuceInitialiser_GUI gui;

    if (argc == 5 && juce::String (argv[1]) == "--screenshots")
        return screenshots (juce::File (argv[2]), argv[3], argv[4]);

    // Nothing real may be launched or contacted from a test.
    auto scratch = juce::File::getSpecialLocation (juce::File::tempDirectory)
                     .getChildFile ("audio-backuper-test-" + juce::Uuid().toString());
    scratch.createDirectory();
    setEnv ("AUDIO_BACKUPER_DATA_DIR", scratch.getFullPathName());
    setEnv ("AUDIO_BACKUPER_HELPER", scratch.getChildFile ("no-such-helper").getFullPathName());

    for (auto& layout : { std::make_pair (juce::AudioChannelSet::mono(), "mono"),
                          std::make_pair (juce::AudioChannelSet::stereo(), "stereo"),
                          std::make_pair (juce::AudioChannelSet::create5point1(), "5.1"),
                          std::make_pair (juce::AudioChannelSet::create7point1point4(), "7.1.4") })
    {
        passThrough<float> (layout.first, juce::String (layout.second) + " float");
        passThrough<double> (layout.first, juce::String (layout.second) + " double");
    }

    {
        AudioBackuperProcessor p;
        juce::AudioProcessor::BusesLayout uneven;
        uneven.inputBuses.add (juce::AudioChannelSet::mono());
        uneven.outputBuses.add (juce::AudioChannelSet::stereo());
        expect (! p.checkBusesLayoutSupported (uneven), "mono-in stereo-out is refused: it would not be a pass-through");
    }

    {
        WatchSpec spec;
        spec.id = "0123456789abcdef";
        spec.localPath = juce::String::fromUTF8 ("/Users/me/Music/\xd0\xa0\xd0\xb5\xd0\xbd\xd0\xb4\xd0\xb5\xd1\x80\xd1\x8b \"final\" & co");
        spec.accountId = "yandex:Test Musician";
        spec.remoteFolder = "My Song/Stems";
        spec.recursive = false;
        spec.overwrite = true;

        AudioBackuperProcessor a, b;
        a.getClient().setWatch (spec);
        juce::MemoryBlock state;
        a.getStateInformation (state);
        b.setStateInformation (state.getData(), (int) state.getSize());
        expect (b.getClient().getWatch() == spec, "the folder binding survives a save and reload of the project");

        auto text = state.toString() + juce::String::createStringFromData (state.getData(), (int) state.getSize());
        expect (! text.containsIgnoreCase ("token") && ! text.containsIgnoreCase ("secret"),
                "the saved state mentions no credentials");

        AudioBackuperProcessor c;
        c.setStateInformation ("garbage", 7);
        expect (c.getClient().getWatch() == WatchSpec(), "unreadable state leaves the plugin unconfigured");
    }

    expect (HelperClient::olderThan ("0.2.0", "0.10.0"), "0.2.0 is older than 0.10.0");
    expect (HelperClient::olderThan ("0.2", "0.2.1"), "0.2 is older than 0.2.1");
    expect (! HelperClient::olderThan ("1.0.0", "0.9.9"), "1.0.0 is not older than 0.9.9");
    expect (! HelperClient::olderThan ("0.2.0", "0.2.0"), "a version is not older than itself");

    {
        // With no helper to reach, creating, using and removing the plugin
        // must still be quick: a DAW closing a project does this for every instance.
        auto start = juce::Time::getMillisecondCounterHiRes();
        {
            AudioBackuperProcessor p;
            p.prepareToPlay (48000.0, 512);
            juce::Thread::sleep (300);
            expect (! p.getClient().getSnapshot().connected, "no helper is reported as connected");
        }
        auto removal = juce::Time::getMillisecondCounterHiRes() - start - 300.0;
        expect (removal < 400.0, "removing the plugin took " + juce::String (removal, 0) + " ms");

        start = juce::Time::getMillisecondCounterHiRes();
        for (int i = 0; i < 50; ++i)
        {
            AudioBackuperProcessor scanned; // what a plugin scan does
        }
        auto scan = juce::Time::getMillisecondCounterHiRes() - start;
        expect (scan < 500.0, "50 scan-style instantiations took " + juce::String (scan, 0) + " ms");
        expect (! scratch.getChildFile ("helper.json").existsAsFile(), "a scan does not start the helper");
    }

    scratch.deleteRecursively();
    std::cout << (failures == 0 ? "All processor checks passed" : juce::String (failures) + " checks failed") << std::endl;
    return failures == 0 ? 0 : 1;
}
