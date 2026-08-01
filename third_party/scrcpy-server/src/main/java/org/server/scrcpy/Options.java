package org.server.scrcpy;

public class Options {

    private String ip;
    private int maxSize;
    private int bitRate;
    private boolean tunnelForward;

    private boolean enableAudioForward = true;

    public String getIp() {
        return ip;
    }

    public void setIp(String ip) {
        this.ip = ip;
    }

    public boolean isEnableAudioForward() {
        return enableAudioForward;
    }

    public void setEnableAudioForward(boolean enableAudioForward) {
        this.enableAudioForward = enableAudioForward;
    }

    public int getMaxSize() {
        return maxSize;
    }

    public void setMaxSize(int maxSize) {
        this.maxSize = maxSize;
    }

    public int getBitRate() {
        return bitRate;
    }

    public void setBitRate(int bitRate) {
        this.bitRate = bitRate;
    }

    public boolean isTunnelForward() {
        return tunnelForward;
    }

    public void setTunnelForward(boolean tunnelForward) {
        this.tunnelForward = tunnelForward;
    }


    /***
     * 从 args 中还原参数
     * @param args
     * @return
     */
    public static Options createOptions(String[] args) {
        if (args == null || args.length != 5) {
            throw new IllegalArgumentException("expected ip, maxSize, bitRate, tunnelForward, enableAudioForward");
        }
        Options options = new Options();
        options.ip = args[0];
        options.maxSize = Integer.parseInt(args[1]) & ~7;
        options.bitRate = Integer.parseInt(args[2]);
        options.tunnelForward = Boolean.parseBoolean(args[3]);
        options.enableAudioForward = Boolean.parseBoolean(args[4]);
        return options;
    }

    /**
     * 将参数转为 args 传递给 scrcpy server
     * @return
     */
    public String[] optionsToArgs() {
        return new String[]{
                ip,
                Long.toString(maxSize),
                Long.toString(bitRate),
                String.valueOf(tunnelForward),
                String.valueOf(enableAudioForward)
        };
    }
}
