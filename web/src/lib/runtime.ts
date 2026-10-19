// 这个外壳是怎么打开的（App 拿到 status 时设）：远程访问（手机经隧道）、服务端部署时，用户不在这台电脑前面，
// 「用系统程序打开」「在访达中显示」这类会在电脑上开窗口的操作要藏起来。
export const runtime = { remote: false, server: false }

/** 能不能在这台电脑上用系统程序打开文件（只有坐在电脑前面时） */
export const canOpenLocal = () => !runtime.remote && !runtime.server
