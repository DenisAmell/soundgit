#include "PluginProcessor.h"
#include "PluginEditor.h"

AudioBackuperProcessor::AudioBackuperProcessor()
#ifndef JucePlugin_PreferredChannelConfigurations
    : AudioProcessor (BusesProperties()
        .withInput  ("Input",  juce::AudioChannelSet::stereo(), true)
        .withOutput ("Output", juce::AudioChannelSet::stereo(), true))
#endif
{
    
}

void AudioBackuperProcessor::prepareToPlay(double, int) {
    backuperService.startIfNotRunning();
}

AudioBackuperProcessor::~AudioBackuperProcessor()
{
    backuperService.stop();
}

juce::AudioProcessorEditor* AudioBackuperProcessor::createEditor()
{
    return new AudioBackuperEditor(*this);
}

void AudioBackuperProcessor::getStateInformation(juce::MemoryBlock& destData)
{
    // Сохраняем watchFolder и другие настройки в проект DAW
    if (auto xml = state.createXml())
        copyXmlToBinary(*xml, destData);
}

void AudioBackuperProcessor::setStateInformation(const void* data, int sizeInBytes)
{
    if (auto xml = getXmlFromBinary(data, sizeInBytes))
        state = juce::ValueTree::fromXml(*xml);

    // Восстанавливаем настройки в сервисе
    auto watchFolder = state.getProperty("watchFolder").toString();
    if (watchFolder.isNotEmpty())
        backuperService.setWatchFolder(watchFolder);
}

// ── Точка входа плагина ───────────────────────────────────────────────────────
juce::AudioProcessor* JUCE_CALLTYPE createPluginFilter()
{
    return new AudioBackuperProcessor();
}
