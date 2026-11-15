#include "PluginProcessor.h"
#include "PluginEditor.h"

AudioBackuperProcessor::AudioBackuperProcessor()
    : AudioProcessor (BusesProperties()
                        .withInput  ("Input",  juce::AudioChannelSet::stereo(), true)
                        .withOutput ("Output", juce::AudioChannelSet::stereo(), true))
{
}

void AudioBackuperProcessor::prepareToPlay (double, int)
{
    // Only starts a low-priority thread; nothing is waited for here.
    client.start();
}

bool AudioBackuperProcessor::isBusesLayoutSupported (const BusesLayout& layouts) const
{
    // Any format, as long as what comes in goes out: mono, stereo, surround.
    auto in = layouts.getMainInputChannelSet();
    return ! in.isDisabled() && in == layouts.getMainOutputChannelSet();
}

juce::AudioProcessorEditor* AudioBackuperProcessor::createEditor()
{
    client.start();
    return new AudioBackuperEditor (*this);
}

juce::ValueTree AudioBackuperProcessor::toTree (const WatchSpec& w)
{
    juce::ValueTree t ("AudioBackuper");
    t.setProperty ("version", 1, nullptr);
    t.setProperty ("watchId", w.id, nullptr);
    t.setProperty ("localPath", w.localPath, nullptr);
    t.setProperty ("accountId", w.accountId, nullptr);
    t.setProperty ("remoteFolder", w.remoteFolder, nullptr);
    t.setProperty ("recursive", w.recursive, nullptr);
    t.setProperty ("overwrite", w.overwrite, nullptr);
    return t;
}

WatchSpec AudioBackuperProcessor::fromTree (const juce::ValueTree& t)
{
    WatchSpec w;
    if (! t.hasType ("AudioBackuper"))
        return w;
    w.id = t.getProperty ("watchId").toString();
    w.localPath = t.getProperty ("localPath").toString();
    w.accountId = t.getProperty ("accountId").toString();
    w.remoteFolder = t.getProperty ("remoteFolder").toString();
    w.recursive = t.getProperty ("recursive", true);
    w.overwrite = t.getProperty ("overwrite", false);
    return w;
}

void AudioBackuperProcessor::getStateInformation (juce::MemoryBlock& destData)
{
    if (auto xml = toTree (client.getWatch()).createXml())
        copyXmlToBinary (*xml, destData);
}

void AudioBackuperProcessor::setStateInformation (const void* data, int sizeInBytes)
{
    if (auto xml = getXmlFromBinary (data, sizeInBytes))
    {
        client.setWatch (fromTree (juce::ValueTree::fromXml (*xml)));
        client.start();
    }
}

juce::AudioProcessor* JUCE_CALLTYPE createPluginFilter()
{
    return new AudioBackuperProcessor();
}
