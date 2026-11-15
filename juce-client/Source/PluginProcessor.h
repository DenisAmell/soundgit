#pragma once

#include <juce_audio_processors/juce_audio_processors.h>
#include "HelperClient.h"

/**
    Audio Backuper: a plugin that does nothing to the sound.

    It exists so that the backup of a project's renders starts with the
    project and its state can be seen inside the DAW. Audio passes through
    untouched; all work happens in a background helper process.
*/
class AudioBackuperProcessor : public juce::AudioProcessor
{
public:
    AudioBackuperProcessor();
    ~AudioBackuperProcessor() override = default;

    void prepareToPlay (double, int) override;
    void releaseResources() override {}
    bool isBusesLayoutSupported (const BusesLayout&) const override;

    // The buffers already hold the input: leaving them alone is a bit-exact pass-through.
    void processBlock (juce::AudioBuffer<float>&, juce::MidiBuffer&) override {}
    void processBlock (juce::AudioBuffer<double>&, juce::MidiBuffer&) override {}
    bool supportsDoublePrecisionProcessing() const override { return true; }

    bool hasEditor() const override { return true; }
    juce::AudioProcessorEditor* createEditor() override;

    const juce::String getName() const override { return "Audio Backuper"; }
    bool acceptsMidi() const override { return false; }
    bool producesMidi() const override { return false; }
    bool isMidiEffect() const override { return false; }
    double getTailLengthSeconds() const override { return 0.0; }

    int getNumPrograms() override { return 1; }
    int getCurrentProgram() override { return 0; }
    void setCurrentProgram (int) override {}
    const juce::String getProgramName (int) override { return {}; }
    void changeProgramName (int, const juce::String&) override {}

    /** The folder binding is saved with the DAW project. It holds no secrets. */
    void getStateInformation (juce::MemoryBlock&) override;
    void setStateInformation (const void*, int) override;

    static juce::ValueTree toTree (const WatchSpec&);
    static WatchSpec fromTree (const juce::ValueTree&);

    HelperClient& getClient() { return client; }

private:
    HelperClient client;

    JUCE_DECLARE_NON_COPYABLE_WITH_LEAK_DETECTOR (AudioBackuperProcessor)
};
