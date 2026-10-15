#pragma once

#include <juce_audio_processors/juce_audio_processors.h>
#include "BackuperService.h"

/**
 * AudioBackuperProcessor
 *
 * Плагин-оболочка без аудио обработки.
 * Вся логика — в BackuperService.
 *
 * processBlock() полностью пуст — нагрузка на DAW минимальна.
 */
class AudioBackuperProcessor : public juce::AudioProcessor
{
public:
    AudioBackuperProcessor();
    ~AudioBackuperProcessor() override;

    // ── AudioProcessor overrides ──────────────────────────────────────────────
    void prepareToPlay(double sampleRate, int samplesPerBlock) override;
    void releaseResources() override {}
    void processBlock(juce::AudioBuffer<float>&, juce::MidiBuffer&) override {}

    bool hasEditor() const override { return true; }
    juce::AudioProcessorEditor* createEditor() override;

    const juce::String getName() const override { return "Audio Backuper"; }
    bool acceptsMidi() const override { return false; }
    bool producesMidi() const override { return false; }
    bool isMidiEffect() const override { return false; }
    double getTailLengthSeconds() const override { return 0.0; }

    int getNumPrograms() override { return 1; }
    int getCurrentProgram() override { return 0; }
    void setCurrentProgram(int) override {}
    const juce::String getProgramName(int) override { return {}; }
    void changeProgramName(int, const juce::String&) override {}

    // ── State (сохраняется в проект DAW) ─────────────────────────────────────
    void getStateInformation(juce::MemoryBlock& destData) override;
    void setStateInformation(const void* data, int sizeInBytes) override;

    // ── Доступ к сервису ─────────────────────────────────────────────────────
    BackuperService& getBackuperService() { return backuperService; }

private:
    BackuperService backuperService;

    // Настройки, которые сохраняются в проект DAW
    juce::ValueTree state { "BackuperState" };

    JUCE_DECLARE_NON_COPYABLE_WITH_LEAK_DETECTOR(AudioBackuperProcessor)
};
